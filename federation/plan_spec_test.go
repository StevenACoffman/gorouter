package federation_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/StevenACoffman/gorouter/federation"
)

// TestBuildPlanSpec_RoundTrip verifies that BuildPlanSpec → json.Marshal →
// json.Unmarshal → Resolve → Execute produces the same merged response as
// BuildPlan → Execute for each of the 5 golden fixtures.
func TestBuildPlanSpec_RoundTrip(t *testing.T) {
	sdlTemplate := mustReadFile(t, filepath.Join("testdata", "golden", "supergraph.graphql"))

	entries, err := os.ReadDir(filepath.Join("testdata", "golden"))
	if err != nil {
		t.Skip("testdata/golden not present")
	}

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join("testdata", "golden", e.Name())
		name := e.Name()
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			runPlanSpecRoundTrip(t, sdlTemplate, dir)
		})
	}
}

func runPlanSpecRoundTrip(t *testing.T, sdlTemplate, dir string) {
	t.Helper()

	query := mustReadFile(t, filepath.Join(dir, "query.graphql"))

	var variables map[string]interface{}
	if data, err := os.ReadFile(filepath.Join(dir, "variables.json")); err == nil {
		_ = json.Unmarshal(data, &variables)
		if len(variables) == 0 {
			variables = nil
		}
	}

	// Build per-subgraph mock servers with sequential response files.
	respDir := filepath.Join(dir, "subgraph_responses")
	entries, err := os.ReadDir(respDir)
	if err != nil {
		t.Fatalf("read subgraph_responses: %v", err)
	}

	seqFiles := make(map[string][]string)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		base := strings.TrimSuffix(e.Name(), ".json")
		var enum string
		if idx := strings.LastIndex(base, "_"); idx >= 0 {
			if _, err2 := fmt.Sscanf(base[idx+1:], "%d", new(int)); err2 == nil {
				enum = base[:idx]
			} else {
				enum = base
			}
		} else {
			enum = base
		}
		seqFiles[enum] = append(seqFiles[enum], filepath.Join(respDir, e.Name()))
	}
	for enum := range seqFiles {
		sort.Slice(seqFiles[enum], func(i, j int) bool {
			return seqFiles[enum][i] < seqFiles[enum][j]
		})
	}

	servers := make(map[string]*httptest.Server)
	for enum, files := range seqFiles {
		bodies := make([][]byte, 0, len(files))
		for _, f := range files {
			raw := mustReadBytes(t, f)
			var wrapper struct {
				Response json.RawMessage `json:"response"`
			}
			if json.Unmarshal(raw, &wrapper) == nil && len(wrapper.Response) > 0 {
				raw = wrapper.Response
			}
			bodies = append(bodies, raw)
		}
		var counter atomic.Int64
		capturedBodies := bodies
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			idx := int(counter.Add(1)) - 1
			if idx >= len(capturedBodies) {
				idx = len(capturedBodies) - 1
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(capturedBodies[idx])
		}))
		t.Cleanup(srv.Close)
		servers[enum] = srv
	}

	sdl := sdlTemplate
	for enum, srv := range servers {
		sdl = strings.ReplaceAll(sdl, enum+"_URL", srv.URL)
	}

	sg, err := federation.ParseSchema(sdl)
	if err != nil {
		t.Fatalf("ParseSchema: %v", err)
	}

	// Build the spec, round-trip through JSON, resolve to a *Plan.
	spec, err := federation.BuildPlanSpec(sg, query, "")
	if err != nil {
		t.Fatalf("BuildPlanSpec: %v", err)
	}

	specJSON, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal PlanSpec: %v", err)
	}

	var decoded federation.PlanSpec
	if err := json.Unmarshal(specJSON, &decoded); err != nil {
		t.Fatalf("unmarshal PlanSpec: %v", err)
	}

	plan, err := decoded.Resolve(sg)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	data, errs, err := federation.Execute(context.Background(), plan, variables, http.DefaultClient)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	actual := map[string]interface{}{"data": data}
	if len(errs) > 0 {
		actual["errors"] = errs
	}
	actualBytes := mustMarshalIndent(t, actual)

	expected := mustReadBytes(t, filepath.Join(dir, "expected.json"))
	if !jsonEqual(expected, actualBytes) {
		t.Errorf("round-trip response mismatch\nwant: %s\n got: %s",
			normalize(expected), normalize(actualBytes))
	}
}

// TestPlanSpec_Resolve_UnknownEnum verifies that Resolve returns an error when
// a spec references a subgraph enum that is not present in the supergraph.
func TestPlanSpec_Resolve_UnknownEnum(t *testing.T) {
	// ParseSchema accepts placeholder URL strings; no HTTP connection is made.
	sdl := mustReadFile(t, filepath.Join("testdata", "golden", "supergraph.graphql"))
	sg, err := federation.ParseSchema(sdl)
	if err != nil {
		t.Fatalf("ParseSchema: %v", err)
	}

	spec := &federation.PlanSpec{
		Fetches: []*federation.FetchSpecData{
			{SubgraphEnum: "NONEXISTENT", Query: "{ product { id } }"},
		},
	}
	_, resolveErr := spec.Resolve(sg)
	if resolveErr == nil {
		t.Error("Resolve with unknown enum should return an error")
	}
	if !strings.Contains(resolveErr.Error(), "NONEXISTENT") {
		t.Errorf("error should name the unknown enum, got: %v", resolveErr)
	}
}

// TestPlanToSpec_EmptyEntityFetches verifies that a plan with no entity fetches
// produces a PlanSpec whose EntityFetches is nil (omitted from JSON, not []).
// Fixture 01 is a single-subgraph query; BuildPlanSpec only parses GraphQL,
// so no HTTP connection is made.
func TestPlanToSpec_EmptyEntityFetches(t *testing.T) {
	// ParseSchema accepts placeholder URL strings; no HTTP connection is made.
	sdl := mustReadFile(t, filepath.Join("testdata", "golden", "supergraph.graphql"))
	sg, err := federation.ParseSchema(sdl)
	if err != nil {
		t.Fatalf("ParseSchema: %v", err)
	}

	query := mustReadFile(t, filepath.Join("testdata", "golden", "01_product_id_sku", "query.graphql"))
	spec, err := federation.BuildPlanSpec(sg, query, "")
	if err != nil {
		t.Fatalf("BuildPlanSpec: %v", err)
	}

	if spec.EntityFetches != nil {
		t.Errorf("expected EntityFetches to be nil for a single-subgraph query, got %v", spec.EntityFetches)
	}

	b, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), "entityFetches") {
		t.Errorf("JSON should omit entityFetches when nil, got: %s", b)
	}
}

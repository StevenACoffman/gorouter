package federation_test

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
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

var update = flag.Bool("update", false, "update golden expected.json files from Go router output")

// TestGolden replays recorded (query, subgraph responses) → expected merged response
// fixtures against the Go router's BuildPlan+Execute pipeline.
// To record new fixtures: run scripts/record_golden.sh with Apollo Router running.
// To update expected files after a fix: go test ./internal/federation/... -run TestGolden -update
func TestGolden(t *testing.T) {
	sdlTemplate := mustReadFile(t, filepath.Join("testdata", "golden", "supergraph.graphql"))

	entries, err := os.ReadDir(filepath.Join("testdata", "golden"))
	if err != nil {
		t.Skip("testdata/golden not present; run scripts/record_golden.sh first")
	}

	cases := map[string]string{}
	for _, e := range entries {
		if e.IsDir() {
			cases[e.Name()] = filepath.Join("testdata", "golden", e.Name())
		}
	}
	if len(cases) == 0 {
		t.Skip("no golden fixtures found")
	}

	for name, dir := range cases {
		name, dir := name, dir
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			runGoldenFixture(t, sdlTemplate, dir)
		})
	}
}

func runGoldenFixture(t *testing.T, sdlTemplate, dir string) {
	t.Helper()

	query := mustReadFile(t, filepath.Join(dir, "query.graphql"))

	var variables map[string]interface{}
	if data, err := os.ReadFile(filepath.Join(dir, "variables.json")); err == nil {
		_ = json.Unmarshal(data, &variables)
		if len(variables) == 0 {
			variables = nil
		}
	}

	// Build one mock httptest.Server per subgraph.
	// Responses are served in order: SUBGRAPH.json on call 1, SUBGRAPH_2.json on
	// call 2, SUBGRAPH_3.json on call 3, etc. (for @requires multi-step plans).
	respDir := filepath.Join(dir, "subgraph_responses")
	entries, err := os.ReadDir(respDir)
	if err != nil {
		t.Fatalf("read subgraph_responses: %v", err)
	}

	// Collect response files grouped by subgraph enum name.
	seqFiles := make(map[string][]string) // enum → ordered response file paths
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		base := strings.TrimSuffix(e.Name(), ".json")
		var enum string
		// Files: SUBGRAPH.json (call 1), SUBGRAPH_2.json (call 2), etc.
		if idx := strings.LastIndex(base, "_"); idx >= 0 {
			if _, err := fmt.Sscanf(base[idx+1:], "%d", new(int)); err == nil {
				enum = base[:idx]
			} else {
				enum = base
			}
		} else {
			enum = base
		}
		seqFiles[enum] = append(seqFiles[enum], filepath.Join(respDir, e.Name()))
	}
	// Sort each subgraph's files: SUBGRAPH.json first, then SUBGRAPH_2.json, etc.
	for enum := range seqFiles {
		sort.Slice(seqFiles[enum], func(i, j int) bool {
			return seqFiles[enum][i] < seqFiles[enum][j]
		})
	}

	servers := map[string]*httptest.Server{} // enum name → server
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
		capturedBodies := bodies // capture for closure
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

	// Patch SDL placeholder URLs with actual server addresses.
	sdl := sdlTemplate
	for enum, srv := range servers {
		sdl = strings.ReplaceAll(sdl, enum+"_URL", srv.URL)
	}

	sg, err := federation.ParseSchema(sdl)
	if err != nil {
		t.Fatalf("ParseSchema: %v", err)
	}
	plan, err := federation.BuildPlan(sg, query, "")
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
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

	goldenPath := filepath.Join(dir, "expected.json")
	if *update {
		if err := os.WriteFile(goldenPath, actualBytes, 0o644); err != nil {
			t.Fatalf("write golden %s: %v", goldenPath, err)
		}
		t.Logf("updated %s", goldenPath)
		return
	}

	expected := mustReadBytes(t, goldenPath)
	if !jsonEqual(expected, actualBytes) {
		t.Errorf("response mismatch\nwant: %s\n got: %s",
			normalize(expected), normalize(actualBytes))
	}
}

// --- test helpers ---

func mustReadFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func mustReadBytes(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return b
}

func mustMarshalIndent(t *testing.T, v interface{}) []byte {
	t.Helper()
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

// jsonEqual compares two JSON byte slices for semantic equality, ignoring key order.
func jsonEqual(a, b []byte) bool {
	var av, bv interface{}
	if err := json.Unmarshal(a, &av); err != nil {
		return false
	}
	if err := json.Unmarshal(b, &bv); err != nil {
		return false
	}
	an, _ := json.Marshal(av)
	bn, _ := json.Marshal(bv)
	return bytes.Equal(an, bn)
}

func normalize(b []byte) []byte {
	var v interface{}
	if err := json.Unmarshal(b, &v); err != nil {
		return b
	}
	out, _ := json.MarshalIndent(v, "", "  ")
	return out
}

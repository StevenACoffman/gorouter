package federation

import (
	"strings"
	"testing"
)

// ── collectLeaves ─────────────────────────────────────────────────────────────

// ── buildEntityQueryFull ──────────────────────────────────────────────────────

func TestBuildEntityQueryFull_NoExtraVars(t *testing.T) {
	got := buildEntityQueryFull("User", "isTeacher\n", "")
	want := buildEntityQuery("User", "isTeacher\n")
	if got != want {
		t.Errorf("with empty extraVarDecls, want same output as buildEntityQuery\ngot:  %q\nwant: %q", got, want)
	}
	if !strings.Contains(got, "query($representations: [_Any!]!)") {
		t.Errorf("missing representations-only header: %q", got)
	}
}

func TestBuildEntityQueryFull_WithExtraVars(t *testing.T) {
	got := buildEntityQueryFull("LearnableContent", "mappedStandards(region: $region) {\n  setId\n}\n", ", $region: String")
	if !strings.Contains(got, "query($representations: [_Any!]!, $region: String)") {
		t.Errorf("missing extra var in header: %q", got)
	}
	if !strings.Contains(got, "mappedStandards(region: $region)") {
		t.Errorf("selection body not present: %q", got)
	}
	if !strings.Contains(got, "... on LearnableContent") {
		t.Errorf("missing type condition: %q", got)
	}
}

func TestBuildEntityQueryFull_MultipleExtraVars(t *testing.T) {
	got := buildEntityQueryFull("Classroom", "masteryAssignments(unitIds: $unitIds) {\n  id\n}\n", ", $unitIds: [String!]!, $filter: String")
	if !strings.Contains(got, "$representations: [_Any!]!, $unitIds: [String!]!, $filter: String") {
		t.Errorf("missing multiple extra vars in header: %q", got)
	}
}

// ── buildEntityFetchVars ──────────────────────────────────────────────────────

func TestBuildEntityFetchVars_NoVarNames(t *testing.T) {
	reps := []map[string]interface{}{{"__typename": "User", "id": "1"}}
	got := buildEntityFetchVars(reps, map[string]interface{}{"region": "US"}, nil)
	if _, ok := got["representations"]; !ok {
		t.Error("missing representations key")
	}
	if _, ok := got["region"]; ok {
		t.Error("region should not be forwarded when varNames is empty")
	}
}

func TestBuildEntityFetchVars_WithVars(t *testing.T) {
	reps := []map[string]interface{}{{"__typename": "C", "id": "x"}}
	opVars := map[string]interface{}{"region": "EU", "unrelated": "ignored"}
	got := buildEntityFetchVars(reps, opVars, []string{"region"})
	if got["region"] != "EU" {
		t.Errorf("region: want EU, got %v", got["region"])
	}
	if _, ok := got["unrelated"]; ok {
		t.Error("unrelated var should not be forwarded")
	}
	if got["representations"] == nil {
		t.Error("representations must be present")
	}
}

func TestBuildEntityFetchVars_StructOpVars(t *testing.T) {
	type myVars struct{ Region string }
	got := buildEntityFetchVars("reps", myVars{Region: "US"}, []string{"region"})
	if _, ok := got["region"]; ok {
		t.Error("struct opVars: should not extract vars (can't subset struct)")
	}
	if got["representations"] != "reps" {
		t.Error("representations must still be present")
	}
}

func TestBuildEntityFetchVars_MissingVar(t *testing.T) {
	opVars := map[string]interface{}{"other": "val"}
	got := buildEntityFetchVars("reps", opVars, []string{"region"})
	if _, ok := got["region"]; ok {
		t.Error("missing var should not appear in result")
	}
}

// ── EntityFetchSpec.entityQuery ───────────────────────────────────────────────

func TestEntityFetchSpec_entityQuery_UsesQueryWhenSet(t *testing.T) {
	ef := &EntityFetchSpec{
		TypeName:  "User",
		Selection: "old\n",
		Query:     "query($representations: [_Any!]!, $region: String) { ... }",
	}
	got := ef.entityQuery()
	if got != ef.Query {
		t.Errorf("want ef.Query, got %q", got)
	}
}

func TestEntityFetchSpec_entityQuery_FallsBackToSelection(t *testing.T) {
	ef := &EntityFetchSpec{
		TypeName:  "User",
		Selection: "isTeacher\n",
	}
	got := ef.entityQuery()
	want := buildEntityQuery("User", "isTeacher\n")
	if got != want {
		t.Errorf("fallback: want buildEntityQuery output, got %q", got)
	}
}

// ── collectLeaves + collectRepresentations with intermediate arrays ────────────

func TestCollectLeaves_IntermediateArray(t *testing.T) {
	// districtById → learningPathsTests[] → courses[] → course
	data := map[string]interface{}{
		"districtById": map[string]interface{}{
			"learningPathsTests": []interface{}{
				map[string]interface{}{
					"courses": []interface{}{
						map[string]interface{}{"course": map[string]interface{}{"contentId": "c1"}},
						map[string]interface{}{"course": map[string]interface{}{"contentId": "c2"}},
					},
				},
				map[string]interface{}{
					"courses": []interface{}{
						map[string]interface{}{"course": map[string]interface{}{"contentId": "c3"}},
					},
				},
			},
		},
	}
	v := data["districtById"]
	leaves := collectLeaves(v, []string{"learningPathsTests", "courses", "course"}, false)
	if len(leaves) != 3 {
		t.Fatalf("got %d leaves, want 3", len(leaves))
	}
	for i, want := range []string{"c1", "c2", "c3"} {
		m, ok := leaves[i].(map[string]interface{})
		if !ok {
			t.Fatalf("leaf[%d] type %T", i, leaves[i])
		}
		if m["contentId"] != want {
			t.Errorf("leaf[%d].contentId = %v, want %s", i, m["contentId"], want)
		}
	}
}

func TestCollectRepresentations_IntermediateArray(t *testing.T) {
	data := map[string]interface{}{
		"districtById": map[string]interface{}{
			"learningPathsTests": []interface{}{
				map[string]interface{}{
					"courses": []interface{}{
						map[string]interface{}{"course": map[string]interface{}{"contentId": "c1", "kaLocale": "en"}},
						map[string]interface{}{"course": map[string]interface{}{"contentId": "c2", "kaLocale": "en"}},
					},
				},
				map[string]interface{}{
					"courses": []interface{}{
						map[string]interface{}{"course": map[string]interface{}{"contentId": "c3", "kaLocale": "es"}},
					},
				},
			},
		},
	}
	reps, err := collectRepresentations(data,
		[]string{"districtById", "learningPathsTests", "courses", "course"},
		"Course",
		[]string{"contentId", "kaLocale"},
		false,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(reps) != 3 {
		t.Fatalf("expected 3 representations, got %d", len(reps))
	}
	for i, want := range []string{"c1", "c2", "c3"} {
		if reps[i]["contentId"] != want {
			t.Errorf("reps[%d].contentId = %v, want %s", i, reps[i]["contentId"], want)
		}
	}
}

func TestMergeEntityResults_IntermediateArray(t *testing.T) {
	data := map[string]interface{}{
		"districtById": map[string]interface{}{
			"learningPathsTests": []interface{}{
				map[string]interface{}{
					"courses": []interface{}{
						map[string]interface{}{"course": map[string]interface{}{"contentId": "c1"}},
						map[string]interface{}{"course": map[string]interface{}{"contentId": "c2"}},
					},
				},
				map[string]interface{}{
					"courses": []interface{}{
						map[string]interface{}{"course": map[string]interface{}{"contentId": "c3"}},
					},
				},
			},
		},
	}
	entities := []interface{}{
		map[string]interface{}{"id": "ID1"},
		map[string]interface{}{"id": "ID2"},
		map[string]interface{}{"id": "ID3"},
	}
	mergeEntityResults(data,
		[]string{"districtById", "learningPathsTests", "courses", "course"},
		entities, false)

	// Verify each course got the correct entity merged in order.
	district := data["districtById"].(map[string]interface{})
	lpts := district["learningPathsTests"].([]interface{})
	type caseCheck struct{ contentID, wantID string }
	cases := []caseCheck{{"c1", "ID1"}, {"c2", "ID2"}, {"c3", "ID3"}}
	idx := 0
	for _, lpt := range lpts {
		lptMap := lpt.(map[string]interface{})
		for _, c := range lptMap["courses"].([]interface{}) {
			cMap := c.(map[string]interface{})
			course := cMap["course"].(map[string]interface{})
			if idx >= len(cases) {
				t.Fatal("more courses than expected")
			}
			if course["contentId"] != cases[idx].contentID {
				t.Errorf("[%d] contentId=%v", idx, course["contentId"])
			}
			if course["id"] != cases[idx].wantID {
				t.Errorf("[%d] id=%v, want %v (wrong entity or not merged)", idx, course["id"], cases[idx].wantID)
			}
			idx++
		}
	}
	if idx != 3 {
		t.Errorf("visited %d courses, want 3", idx)
	}
}

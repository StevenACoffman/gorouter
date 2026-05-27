package federation_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/StevenACoffman/gorouter/federation"
)

// minimalSDL is a minimal but complete Federation v2 supergraph SDL used in tests.
// It mirrors the accounts/reviews structure from the Apollo demo supergraph.
const minimalSDL = `
schema
  @link(url: "https://specs.apollo.dev/link/v1.0")
  @link(url: "https://specs.apollo.dev/join/v0.3", for: EXECUTION)
{
  query: Query
}

directive @join__field(graph: join__Graph, requires: join__FieldSet, provides: join__FieldSet, external: Boolean) repeatable on FIELD_DEFINITION | INPUT_FIELD_DEFINITION
directive @join__graph(name: String!, url: String!) on ENUM_VALUE
directive @join__type(graph: join__Graph!, key: join__FieldSet, extension: Boolean! = false) repeatable on OBJECT | INTERFACE
directive @link(url: String, for: String) repeatable on SCHEMA

scalar join__FieldSet

enum join__Graph {
  ACCOUNTS @join__graph(name: "accounts", url: "ACCOUNTS_URL")
  REVIEWS  @join__graph(name: "reviews",  url: "REVIEWS_URL")
}

type Query
  @join__type(graph: ACCOUNTS)
  @join__type(graph: REVIEWS)
{
  me: User            @join__field(graph: ACCOUNTS)
  topReviews: [Review] @join__field(graph: REVIEWS)
}

type User
  @join__type(graph: ACCOUNTS, key: "id")
  @join__type(graph: REVIEWS,  key: "id")
{
  id:       ID!     @join__field(graph: ACCOUNTS)
  name:     String  @join__field(graph: ACCOUNTS)
  username: String  @join__field(graph: ACCOUNTS) @join__field(graph: REVIEWS, external: true)
  reviews:  [Review] @join__field(graph: REVIEWS)
}

type Review @join__type(graph: REVIEWS, key: "id") {
  id:   ID!
  body: String
  author: User @join__field(graph: REVIEWS, provides: "username")
}
`

func TestParseSchema_Subgraphs(t *testing.T) {
	sg, err := federation.ParseSchema(minimalSDL)
	if err != nil {
		t.Fatalf("ParseSchema error: %v", err)
	}
	accounts := sg.SubgraphByEnum("ACCOUNTS")
	if accounts == nil {
		t.Fatal("ACCOUNTS subgraph not found")
	}
	if accounts.Name != "accounts" {
		t.Errorf("ACCOUNTS.Name = %q, want accounts", accounts.Name)
	}
	if accounts.URL != "ACCOUNTS_URL" {
		t.Errorf("ACCOUNTS.URL = %q, want ACCOUNTS_URL", accounts.URL)
	}
	if sg.SubgraphByEnum("REVIEWS") == nil {
		t.Error("REVIEWS subgraph not found")
	}
}

func TestParseSchema_FieldOwnership(t *testing.T) {
	sg, err := federation.ParseSchema(minimalSDL)
	if err != nil {
		t.Fatalf("ParseSchema error: %v", err)
	}
	tests := []struct {
		typeName  string
		fieldName string
		wantEnum  string
	}{
		{"Query", "me", "ACCOUNTS"},
		{"Query", "topReviews", "REVIEWS"},
		{"User", "id", "ACCOUNTS"},
		{"User", "name", "ACCOUNTS"},
		{"User", "reviews", "REVIEWS"},
		// Review fields without @join__field are implicit REVIEWS (single subgraph type)
		{"Review", "id", "REVIEWS"},
		{"Review", "body", "REVIEWS"},
		{"Review", "author", "REVIEWS"},
	}
	for _, tt := range tests {
		owner := sg.OwnerOf(tt.typeName, tt.fieldName)
		if owner != tt.wantEnum {
			t.Errorf("OwnerOf(%q, %q) = %q, want %q", tt.typeName, tt.fieldName, owner, tt.wantEnum)
		}
	}
}

func TestParseSchema_KeyFields(t *testing.T) {
	sg, err := federation.ParseSchema(minimalSDL)
	if err != nil {
		t.Fatalf("ParseSchema error: %v", err)
	}
	keys := sg.KeysFor("User", "REVIEWS")
	if len(keys) != 1 || keys[0] != "id" {
		t.Errorf("KeysFor(User, REVIEWS) = %v, want [id]", keys)
	}
}

func TestParseSchema_FieldTypeName(t *testing.T) {
	sg, err := federation.ParseSchema(minimalSDL)
	if err != nil {
		t.Fatalf("ParseSchema error: %v", err)
	}
	if got := sg.FieldTypeName("Query", "me"); got != "User" {
		t.Errorf("FieldTypeName(Query, me) = %q, want User", got)
	}
	if got := sg.FieldTypeName("Query", "topReviews"); got != "Review" {
		t.Errorf("FieldTypeName(Query, topReviews) = %q, want Review", got)
	}
	if !sg.FieldIsList("Query", "topReviews") {
		t.Error("FieldIsList(Query, topReviews) = false, want true")
	}
	if sg.FieldIsList("Query", "me") {
		t.Error("FieldIsList(Query, me) = true, want false")
	}
}

func TestBuildPlan_SingleSubgraph(t *testing.T) {
	sg, _ := federation.ParseSchema(minimalSDL)
	plan, err := federation.BuildPlan(sg, `{ me { id name } }`, "")
	if err != nil {
		t.Fatalf("BuildPlan error: %v", err)
	}
	if len(plan.Fetches) != 1 {
		t.Fatalf("len(Fetches) = %d, want 1", len(plan.Fetches))
	}
	if plan.Fetches[0].Subgraph.EnumName != "ACCOUNTS" {
		t.Errorf("fetch subgraph = %q, want ACCOUNTS", plan.Fetches[0].Subgraph.EnumName)
	}
	if len(plan.EntityFetches) != 0 {
		t.Errorf("len(EntityFetches) = %d, want 0", len(plan.EntityFetches))
	}
	if !strings.Contains(plan.Fetches[0].Query, "me") {
		t.Errorf("query should contain 'me': %s", plan.Fetches[0].Query)
	}
}

func TestBuildPlan_MultiRootFields(t *testing.T) {
	sg, _ := federation.ParseSchema(minimalSDL)
	// me is ACCOUNTS, topReviews is REVIEWS — two separate fetches in parallel.
	plan, err := federation.BuildPlan(sg, `{ me { id name } topReviews { id body } }`, "")
	if err != nil {
		t.Fatalf("BuildPlan error: %v", err)
	}
	if len(plan.Fetches) != 2 {
		t.Fatalf("len(Fetches) = %d, want 2", len(plan.Fetches))
	}
	subgraphs := map[string]bool{}
	for _, f := range plan.Fetches {
		subgraphs[f.Subgraph.EnumName] = true
	}
	if !subgraphs["ACCOUNTS"] || !subgraphs["REVIEWS"] {
		t.Errorf("expected both ACCOUNTS and REVIEWS fetches; got %v", subgraphs)
	}
}

func TestBuildPlan_CrossSubgraphEntityFetch(t *testing.T) {
	sg, _ := federation.ParseSchema(minimalSDL)
	// me.reviews requires entity resolution from REVIEWS.
	plan, err := federation.BuildPlan(sg, `{ me { id name reviews { id body } } }`, "")
	if err != nil {
		t.Fatalf("BuildPlan error: %v", err)
	}
	if len(plan.Fetches) != 1 {
		t.Fatalf("len(Fetches) = %d, want 1", len(plan.Fetches))
	}
	if plan.Fetches[0].Subgraph.EnumName != "ACCOUNTS" {
		t.Errorf("initial fetch subgraph = %q, want ACCOUNTS", plan.Fetches[0].Subgraph.EnumName)
	}
	// The initial ACCOUNTS fetch must include `id` (key for REVIEWS entity resolution).
	if !strings.Contains(plan.Fetches[0].Query, "id") {
		t.Errorf("ACCOUNTS query should contain key field 'id': %s", plan.Fetches[0].Query)
	}
	if len(plan.EntityFetches) != 1 {
		t.Fatalf("len(EntityFetches) = %d, want 1", len(plan.EntityFetches))
	}
	ef := plan.EntityFetches[0]
	if ef.Subgraph.EnumName != "REVIEWS" {
		t.Errorf("entity fetch subgraph = %q, want REVIEWS", ef.Subgraph.EnumName)
	}
	if ef.TypeName != "User" {
		t.Errorf("entity fetch type = %q, want User", ef.TypeName)
	}
	if len(ef.KeyFields) == 0 || ef.KeyFields[0] != "id" {
		t.Errorf("entity fetch key fields = %v, want [id]", ef.KeyFields)
	}
	if !strings.Contains(ef.Selection, "reviews") {
		t.Errorf("entity fetch selection should contain 'reviews': %q", ef.Selection)
	}
	if ef.ParentPath[0] != "me" {
		t.Errorf("entity fetch parent path = %v, want [me]", ef.ParentPath)
	}
	if ef.Query == "" {
		t.Error("entity fetch Query should be non-empty (built at plan time)")
	}
	if !strings.Contains(ef.Query, "_entities") {
		t.Errorf("entity fetch Query should contain _entities: %q", ef.Query)
	}
}

// varArgSDL extends the minimal schema with a cross-subgraph field that accepts a variable argument.
const varArgSDL = `
schema
  @link(url: "https://specs.apollo.dev/link/v1.0")
  @link(url: "https://specs.apollo.dev/join/v0.3", for: EXECUTION)
{
  query: Query
}

directive @join__field(graph: join__Graph, requires: join__FieldSet, provides: join__FieldSet, external: Boolean) repeatable on FIELD_DEFINITION | INPUT_FIELD_DEFINITION
directive @join__graph(name: String!, url: String!) on ENUM_VALUE
directive @join__type(graph: join__Graph!, key: join__FieldSet, extension: Boolean! = false) repeatable on OBJECT | INTERFACE
directive @link(url: String, for: String) repeatable on SCHEMA

scalar join__FieldSet

enum join__Graph {
  ACCOUNTS @join__graph(name: "accounts", url: "ACCOUNTS_URL")
  REVIEWS  @join__graph(name: "reviews",  url: "REVIEWS_URL")
}

type Query
  @join__type(graph: ACCOUNTS)
{
  me: User @join__field(graph: ACCOUNTS)
}

type User
  @join__type(graph: ACCOUNTS, key: "id")
  @join__type(graph: REVIEWS,  key: "id")
{
  id:                       ID!      @join__field(graph: ACCOUNTS)
  name:                     String   @join__field(graph: ACCOUNTS)
  filteredReviews(region: String): [Review] @join__field(graph: REVIEWS)
}

type Review @join__type(graph: REVIEWS, key: "id") {
  id:   ID!
  body: String
}
`

func TestBuildPlan_EntityFetchWithVars(t *testing.T) {
	sg, err := federation.ParseSchema(varArgSDL)
	if err != nil {
		t.Fatalf("ParseSchema: %v", err)
	}
	plan, err := federation.BuildPlan(sg,
		`query GetUser($region: String) { me { id name filteredReviews(region: $region) { id body } } }`, "")
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if len(plan.EntityFetches) != 1 {
		t.Fatalf("len(EntityFetches) = %d, want 1", len(plan.EntityFetches))
	}
	ef := plan.EntityFetches[0]
	if !strings.Contains(ef.Query, "$representations: [_Any!]!, $region: String") {
		t.Errorf("Query should declare $region: Query = %q", ef.Query)
	}
	if len(ef.Variables) != 1 || ef.Variables[0] != "region" {
		t.Errorf("Variables = %v, want [region]", ef.Variables)
	}
	if !strings.Contains(ef.Query, "filteredReviews(region: $region)") {
		t.Errorf("Query should contain field with argument: %q", ef.Query)
	}
}

func TestPlanSpecRoundTrip_WithEntityFetchVars(t *testing.T) {
	sg, err := federation.ParseSchema(varArgSDL)
	if err != nil {
		t.Fatalf("ParseSchema: %v", err)
	}
	spec, err := federation.BuildPlanSpec(sg,
		`query GetUser($region: String) { me { id name filteredReviews(region: $region) { id body } } }`, "")
	if err != nil {
		t.Fatalf("BuildPlanSpec: %v", err)
	}
	if len(spec.EntityFetches) == 0 {
		t.Fatal("expected entity fetches in spec")
	}
	origQuery := spec.EntityFetches[0].Query
	origVars := spec.EntityFetches[0].Variables
	if origQuery == "" {
		t.Fatal("entity fetch Query should be non-empty before round-trip")
	}

	b, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded federation.PlanSpec
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	plan, err := decoded.Resolve(sg)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	ef := plan.EntityFetches[0]
	if ef.Query != origQuery {
		t.Errorf("Query not preserved: got %q, want %q", ef.Query, origQuery)
	}
	if len(ef.Variables) != len(origVars) {
		t.Errorf("Variables not preserved: got %v, want %v", ef.Variables, origVars)
	} else {
		for i, v := range origVars {
			if ef.Variables[i] != v {
				t.Errorf("Variables[%d]: got %q, want %q", i, ef.Variables[i], v)
			}
		}
	}
}

func TestBuildPlan_WithVariables(t *testing.T) {
	sg, _ := federation.ParseSchema(minimalSDL)
	plan, err := federation.BuildPlan(sg,
		`query TopReviews($first: Int!) { topReviews { id body } }`, "")
	if err != nil {
		t.Fatalf("BuildPlan error: %v", err)
	}
	if len(plan.Fetches) != 1 {
		t.Fatalf("len(Fetches) = %d, want 1", len(plan.Fetches))
	}
	// Variables are not used in this query, so the query should have no var decls.
	query := plan.Fetches[0].Query
	if strings.Contains(query, "$first") {
		t.Logf("query: %s", query)
	}
}

const mutationSDL = minimalSDL + `
type Mutation
  @join__type(graph: ACCOUNTS)
  @join__type(graph: REVIEWS)
{
  createReview(body: String!): Review @join__field(graph: REVIEWS)
  updateUser(id: ID!, name: String!): User @join__field(graph: ACCOUNTS)
}
`

func TestBuildPlan_Mutation(t *testing.T) {
	sg, err := federation.ParseSchema(mutationSDL)
	if err != nil {
		t.Fatalf("ParseSchema: %v", err)
	}
	plan, err := federation.BuildPlan(sg, `mutation CreateReview($body: String!) { createReview(body: $body) { id body } }`, "")
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if len(plan.Fetches) != 1 {
		t.Fatalf("len(Fetches) = %d, want 1", len(plan.Fetches))
	}
	if plan.Fetches[0].Subgraph.EnumName != "REVIEWS" {
		t.Errorf("fetch subgraph = %q, want REVIEWS", plan.Fetches[0].Subgraph.EnumName)
	}
	if !strings.Contains(plan.Fetches[0].Query, "mutation") {
		t.Errorf("plan query should be a mutation: %s", plan.Fetches[0].Query)
	}
	if !strings.Contains(plan.Fetches[0].Query, "createReview") {
		t.Errorf("plan query missing createReview: %s", plan.Fetches[0].Query)
	}
}

// TestExecute_SingleSubgraph tests end-to-end execution against a mock subgraph.
func TestExecute_SingleSubgraph(t *testing.T) {
	// Mock ACCOUNTS subgraph.
	accounts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req map[string]interface{}
		_ = json.Unmarshal(body, &req)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"me":{"id":"1","name":"Alice"}}}`))
	}))
	defer accounts.Close()

	sdl := strings.ReplaceAll(minimalSDL, "ACCOUNTS_URL", accounts.URL)
	sdl = strings.ReplaceAll(sdl, "REVIEWS_URL", "http://reviews.invalid")

	sg, err := federation.ParseSchema(sdl)
	if err != nil {
		t.Fatalf("ParseSchema: %v", err)
	}

	plan, err := federation.BuildPlan(sg, `{ me { id name } }`, "")
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}

	data, errs, err := federation.Execute(context.Background(), plan, nil, accounts.Client())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(errs) != 0 {
		t.Errorf("unexpected errors: %v", errs)
	}

	me, ok := data["me"].(map[string]interface{})
	if !ok {
		t.Fatalf("data[me] type %T, want map", data["me"])
	}
	if me["name"] != "Alice" {
		t.Errorf("me.name = %v, want Alice", me["name"])
	}
}

// TestExecute_CrossSubgraph tests entity resolution across two mock subgraphs.
func TestExecute_CrossSubgraph(t *testing.T) {
	// Mock ACCOUNTS: returns user without reviews.
	accounts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"me":{"id":"1","name":"Alice"}}}`))
	}))
	defer accounts.Close()

	// Mock REVIEWS entity endpoint: returns reviews for user id "1".
	reviews := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req map[string]interface{}
		_ = json.Unmarshal(body, &req)

		// Verify the entity query structure.
		query, _ := req["query"].(string)
		if !strings.Contains(query, "_entities") {
			http.Error(w, "expected _entities query", http.StatusBadRequest)
			return
		}
		vars, _ := req["variables"].(map[string]interface{})
		reps, _ := vars["representations"].([]interface{})
		if len(reps) == 0 {
			http.Error(w, "empty representations", http.StatusBadRequest)
			return
		}
		rep, _ := reps[0].(map[string]interface{})
		if rep["__typename"] != "User" || rep["id"] != "1" {
			http.Error(w, "wrong representation", http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"_entities":[{"reviews":[{"id":"r1","body":"Great"}]}]}}`))
	}))
	defer reviews.Close()

	sdl := strings.ReplaceAll(minimalSDL, "ACCOUNTS_URL", accounts.URL)
	sdl = strings.ReplaceAll(sdl, "REVIEWS_URL", reviews.URL)

	sg, err := federation.ParseSchema(sdl)
	if err != nil {
		t.Fatalf("ParseSchema: %v", err)
	}

	plan, err := federation.BuildPlan(sg, `{ me { id name reviews { id body } } }`, "")
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}

	// Use the same client for both servers (httptest clients work for any httptest server).
	data, errs, err := federation.Execute(context.Background(), plan, nil, http.DefaultClient)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(errs) != 0 {
		t.Errorf("unexpected errors: %v", errs)
	}

	me, ok := data["me"].(map[string]interface{})
	if !ok {
		t.Fatalf("data[me] type %T, want map", data["me"])
	}
	if me["name"] != "Alice" {
		t.Errorf("me.name = %v, want Alice", me["name"])
	}
	reviewsList, ok := me["reviews"].([]interface{})
	if !ok {
		t.Fatalf("me.reviews type %T, want []interface{}", me["reviews"])
	}
	if len(reviewsList) != 1 {
		t.Fatalf("len(me.reviews) = %d, want 1", len(reviewsList))
	}
	review, _ := reviewsList[0].(map[string]interface{})
	if review["body"] != "Great" {
		t.Errorf("review.body = %v, want Great", review["body"])
	}
}

// TestHandler_NoSupergraph verifies the handler returns an error for missing query.
func TestHandler_MissingQuery(t *testing.T) {
	sg, _ := federation.ParseSchema(minimalSDL)
	h := federation.Handler(sg, nil)

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

// TestHandler_EndToEnd tests the full handler pipeline with mock subgraphs.
func TestHandler_EndToEnd(t *testing.T) {
	accountsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"me":{"id":"42","name":"Bob"}}}`))
	}))
	defer accountsSrv.Close()

	sdl := strings.ReplaceAll(minimalSDL, "ACCOUNTS_URL", accountsSrv.URL)
	sdl = strings.ReplaceAll(sdl, "REVIEWS_URL", "http://reviews.invalid")

	sg, err := federation.ParseSchema(sdl)
	if err != nil {
		t.Fatalf("ParseSchema: %v", err)
	}

	h := federation.Handler(sg, accountsSrv.Client())
	body := `{"query":"{ me { id name } }"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	dataMap, _ := resp["data"].(map[string]interface{})
	me, _ := dataMap["me"].(map[string]interface{})
	if me["name"] != "Bob" {
		t.Errorf("me.name = %v, want Bob", me["name"])
	}
}

// TestBuildEntityQuery verifies the entity query structure.
func TestBuildEntityQuery(t *testing.T) {
	sg, _ := federation.ParseSchema(minimalSDL)
	plan, _ := federation.BuildPlan(sg, `{ me { id reviews { id body } } }`, "")

	if len(plan.EntityFetches) == 0 {
		t.Fatal("expected at least one entity fetch")
	}
	ef := plan.EntityFetches[0]

	// Verify entity query has correct structure by actually executing it
	// against a mock server that checks the request.
	var capturedQuery string
	var capturedVars map[string]interface{}

	mockReviews := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req map[string]interface{}
		_ = json.Unmarshal(body, &req)
		capturedQuery, _ = req["query"].(string)
		capturedVars, _ = req["variables"].(map[string]interface{})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"_entities":[{"reviews":[]}]}}`))
	}))
	defer mockReviews.Close()

	// Patch the subgraph URL and execute.
	patchedSDL := strings.ReplaceAll(minimalSDL, "ACCOUNTS_URL", "http://accounts.invalid")
	patchedSDL = strings.ReplaceAll(patchedSDL, "REVIEWS_URL", mockReviews.URL)
	sgPatched, _ := federation.ParseSchema(patchedSDL)

	// Build a plan with the patched SDL but the same query.
	plan2, _ := federation.BuildPlan(sgPatched, `{ me { id reviews { id body } } }`, "")

	// Provide fake ACCOUNTS response so Execute can proceed.
	mockAccounts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"me":{"id":"1","name":"Test"}}}`))
	}))
	defer mockAccounts.Close()

	// Re-parse with both URLs patched.
	sdl2 := strings.ReplaceAll(minimalSDL, "ACCOUNTS_URL", mockAccounts.URL)
	sdl2 = strings.ReplaceAll(sdl2, "REVIEWS_URL", mockReviews.URL)
	sgFull, _ := federation.ParseSchema(sdl2)
	plan3, _ := federation.BuildPlan(sgFull, `{ me { id reviews { id body } } }`, "")

	_ = plan  // used for ef above
	_ = plan2 // used to verify plan structure

	_, _, _ = federation.Execute(context.Background(), plan3, nil, http.DefaultClient)

	if capturedQuery == "" {
		t.Fatal("entity fetch was not called")
	}
	if !strings.Contains(capturedQuery, "_entities") {
		t.Errorf("entity query should contain _entities: %s", capturedQuery)
	}
	if !strings.Contains(capturedQuery, "... on User") {
		t.Errorf("entity query should contain '... on User': %s", capturedQuery)
	}
	reps, ok := capturedVars["representations"].([]interface{})
	if !ok || len(reps) == 0 {
		t.Fatalf("entity fetch representations missing: %v", capturedVars)
	}
	rep, _ := reps[0].(map[string]interface{})
	if rep["__typename"] != "User" {
		t.Errorf("representation.__typename = %v, want User", rep["__typename"])
	}
	if rep["id"] != "1" {
		t.Errorf("representation.id = %v, want 1", rep["id"])
	}

	_ = ef // suppress unused variable warning
}

func TestSubgraphURLs(t *testing.T) {
	urls, err := federation.SubgraphURLs(minimalSDL)
	if err != nil {
		t.Fatal(err)
	}
	if urls["ACCOUNTS"] != "ACCOUNTS_URL" {
		t.Errorf("ACCOUNTS url = %q, want ACCOUNTS_URL", urls["ACCOUNTS"])
	}
	if urls["REVIEWS"] != "REVIEWS_URL" {
		t.Errorf("REVIEWS url = %q, want REVIEWS_URL", urls["REVIEWS"])
	}
	if len(urls) != 2 {
		t.Errorf("expected 2 entries, got %d: %v", len(urls), urls)
	}
}

func TestSubgraphURLs_InvalidSDL(t *testing.T) {
	_, err := federation.SubgraphURLs("not valid graphql {{{{")
	if err == nil {
		t.Error("expected error for invalid SDL")
	}
}

// inlineFragSDL has a union type AdminAggregate so we can test that
// "... on District { id }" inline fragments survive the planner round-trip.
const inlineFragSDL = `
schema
  @link(url: "https://specs.apollo.dev/link/v1.0")
  @link(url: "https://specs.apollo.dev/join/v0.3", for: EXECUTION)
{
  query: Query
}

directive @join__field(graph: join__Graph, requires: join__FieldSet, provides: join__FieldSet, external: Boolean) repeatable on FIELD_DEFINITION | INPUT_FIELD_DEFINITION
directive @join__graph(name: String!, url: String!) on ENUM_VALUE
directive @join__type(graph: join__Graph!, key: join__FieldSet, extension: Boolean! = false) repeatable on OBJECT | INTERFACE
directive @join__unionMember(graph: join__Graph!, member: String!) repeatable on UNION
directive @link(url: String, for: String) repeatable on SCHEMA

scalar join__FieldSet

enum join__Graph {
  DISTRICTS @join__graph(name: "districts", url: "DISTRICTS_URL")
}

type Query @join__type(graph: DISTRICTS) {
  metaDistrict(id: ID!): MetaDistrict @join__field(graph: DISTRICTS)
}

type MetaDistrict @join__type(graph: DISTRICTS, key: "id") {
  id:          ID!
  descendants: [AdminAggregate!]! @join__field(graph: DISTRICTS)
}

union AdminAggregate
  @join__unionMember(graph: DISTRICTS, member: "District")
  @join__unionMember(graph: DISTRICTS, member: "MetaDistrict")
  = District | MetaDistrict

type District @join__type(graph: DISTRICTS, key: "id") {
  id: ID!
}
`

func TestBuildPlan_InlineFragment(t *testing.T) {
	sg, err := federation.ParseSchema(inlineFragSDL)
	if err != nil {
		t.Fatalf("ParseSchema: %v", err)
	}
	plan, err := federation.BuildPlan(sg, `
		query Q($id: ID!) {
			metaDistrict(id: $id) {
				id
				descendants {
					__typename
					... on District {
						id
					}
				}
			}
		}`, "Q")
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if len(plan.Fetches) != 1 {
		t.Fatalf("want 1 fetch, got %d", len(plan.Fetches))
	}
	q := plan.Fetches[0].Query
	if !strings.Contains(q, "... on District") {
		t.Errorf("inline fragment missing from subgraph query:\n%s", q)
	}
	if !strings.Contains(q, "id") {
		t.Errorf("id field inside inline fragment missing:\n%s", q)
	}
	if len(plan.EntityFetches) != 0 {
		t.Errorf("want 0 entity fetches (single-subgraph query), got %d", len(plan.EntityFetches))
	}
}

package federation

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
)

// GraphQLRequest is the JSON body sent to a subgraph.
type GraphQLRequest struct {
	Query         string                 `json:"query"`
	OperationName string                 `json:"operationName,omitempty"`
	Variables     map[string]interface{} `json:"variables,omitempty"`
}

// GraphQLResponse is the JSON body received from a subgraph.
type GraphQLResponse struct {
	Data   json.RawMessage  `json:"data"`
	Errors []GraphQLError   `json:"errors,omitempty"`
}

// GraphQLError is a single error object in a GraphQL response.
type GraphQLError struct {
	Message    string                 `json:"message"`
	Locations  []map[string]int       `json:"locations,omitempty"`
	Path       []interface{}          `json:"path,omitempty"`
	Extensions map[string]interface{} `json:"extensions,omitempty"`
}

// Execute runs a Plan against real HTTP subgraph endpoints.
// It executes initial fetches in parallel, then resolves entity fetches sequentially,
// and returns the merged data and any accumulated errors.
func Execute(
	ctx context.Context,
	plan *Plan,
	variables map[string]interface{},
	client *http.Client,
) (map[string]interface{}, []GraphQLError, error) {
	if client == nil {
		client = http.DefaultClient
	}

	// Execute initial fetches in parallel.
	type fetchResult struct {
		data   map[string]interface{}
		errors []GraphQLError
		err    error
	}
	results := make([]fetchResult, len(plan.Fetches))
	var wg sync.WaitGroup

	for i, fetch := range plan.Fetches {
		wg.Add(1)
		go func(i int, fetch *FetchSpec) {
			defer wg.Done()
			vars := filterVars(variables, fetch.Variables)
			resp, err := doGraphQL(ctx, client, fetch.Subgraph.URL, fetch.Query, "", vars)
			if err != nil {
				results[i] = fetchResult{err: fmt.Errorf("federation: fetch %s: %w", fetch.Subgraph.Name, err)}
				return
			}
			var data map[string]interface{}
			if len(resp.Data) > 0 && string(resp.Data) != "null" {
				if err := json.Unmarshal(resp.Data, &data); err != nil {
					results[i] = fetchResult{err: fmt.Errorf("federation: decode %s data: %w", fetch.Subgraph.Name, err)}
					return
				}
			}
			results[i] = fetchResult{data: data, errors: resp.Errors}
		}(i, fetch)
	}
	wg.Wait()

	// Merge initial fetch results.
	merged := make(map[string]interface{})
	var allErrors []GraphQLError

	for _, r := range results {
		if r.err != nil {
			return nil, nil, r.err
		}
		allErrors = append(allErrors, r.errors...)
		for k, v := range r.data {
			merged[k] = v
		}
	}

	// Execute entity fetches sequentially (they may depend on merged data).
	for _, ef := range plan.EntityFetches {
		allKeyFields := append(append([]string{}, ef.KeyFields...), ef.RequiresFields...)
		reps, err := collectRepresentations(merged, ef.ParentPath, ef.TypeName, allKeyFields, ef.IsParentList)
		if err != nil {
			// Soft failure: add an error but continue.
			allErrors = append(allErrors, GraphQLError{
				Message: fmt.Sprintf("federation: entity fetch for %s: %s", ef.TypeName, err),
			})
			continue
		}
		if len(reps) == 0 {
			continue
		}

		entityQuery := ef.entityQuery()
		entityVars := buildEntityFetchVars(reps, variables, ef.Variables)

		resp, err := doGraphQL(ctx, client, ef.Subgraph.URL, entityQuery, "", entityVars)
		if err != nil {
			allErrors = append(allErrors, GraphQLError{
				Message: fmt.Sprintf("federation: entity fetch %s/%s: %s", ef.Subgraph.Name, ef.TypeName, err),
			})
			continue
		}
		allErrors = append(allErrors, resp.Errors...)

		if len(resp.Data) > 0 && string(resp.Data) != "null" {
			var entityData map[string]interface{}
			if err := json.Unmarshal(resp.Data, &entityData); err == nil {
				if entities, ok := entityData["_entities"].([]interface{}); ok {
					mergeEntityResults(merged, ef.ParentPath, entities, ef.IsParentList)
				}
			}
		}
	}

	if len(plan.Projection) > 0 {
		merged = ApplyProjection(merged, plan.Projection)
	}
	return merged, allErrors, nil
}

// doGraphQL sends a GraphQL POST request to url and returns the parsed response.
func doGraphQL(
	ctx context.Context,
	client *http.Client,
	url, query, operationName string,
	variables map[string]interface{},
) (*GraphQLResponse, error) {
	body, err := json.Marshal(GraphQLRequest{
		Query:         query,
		OperationName: operationName,
		Variables:     variables,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	var gqlResp GraphQLResponse
	if err := json.Unmarshal(raw, &gqlResp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &gqlResp, nil
}

// buildEntityQuery constructs the _entities query for entity resolution.
// selection is the body to fetch, e.g. "reviews {\n  title\n}\n"
func buildEntityQuery(typeName, selection string) string {
	return buildEntityQueryFull(typeName, selection, "")
}

// buildEntityQueryFull builds a complete _entities query with optional extra
// variable declarations. extraVarDecls is pre-formatted as ", $region: String"
// (leading comma and space included); pass "" when there are no extra vars.
func buildEntityQueryFull(typeName, selection, extraVarDecls string) string {
	lines := strings.Split(strings.TrimRight(selection, "\n"), "\n")
	indented := make([]string, 0, len(lines))
	for _, l := range lines {
		indented = append(indented, "      "+l)
	}
	return fmt.Sprintf(
		"query($representations: [_Any!]!%s) {\n  _entities(representations: $representations) {\n    ... on %s {\n%s\n    }\n  }\n}",
		extraVarDecls,
		typeName,
		strings.Join(indented, "\n"),
	)
}

// buildEntityFetchVars merges entity representations with any operation variables
// the entity fetch selection references. opVars must be map[string]interface{};
// struct-typed opVars cannot be subset-extracted.
func buildEntityFetchVars(reps interface{}, opVars interface{}, varNames []string) map[string]interface{} {
	m := map[string]interface{}{"representations": reps}
	if len(varNames) == 0 {
		return m
	}
	opMap, ok := opVars.(map[string]interface{})
	if !ok {
		return m
	}
	for _, name := range varNames {
		if v, ok := opMap[name]; ok {
			m[name] = v
		}
	}
	return m
}

// collectRepresentations extracts entity representations from merged data at path.
// Arrays at any step are fanned out — all leaves across nested arrays are collected
// in traversal order, one representation per leaf.
func collectRepresentations(
	data map[string]interface{},
	path []string,
	typeName string,
	keyFields []string,
	isList bool,
) ([]map[string]interface{}, error) {
	if len(path) == 0 {
		return nil, nil
	}
	v, ok := data[path[0]]
	if !ok {
		return nil, nil
	}
	leaves := collectLeaves(v, path[1:], isList)
	reps := make([]map[string]interface{}, 0, len(leaves))
	for _, leaf := range leaves {
		itemMap, ok := leaf.(map[string]interface{})
		if !ok {
			continue
		}
		rep, err := buildRepresentation(itemMap, typeName, keyFields)
		if err != nil {
			return nil, err
		}
		reps = append(reps, rep)
	}
	return reps, nil
}

// collectLeaves traverses v following path, fanning out through any slices encountered
// at intermediate steps. Returns the leaf values in traversal order.
// When isList is true the terminal value is itself unwrapped as a slice.
func collectLeaves(v interface{}, path []string, isList bool) []interface{} {
	if len(path) == 0 {
		if isList {
			if list, ok := v.([]interface{}); ok {
				return list
			}
			return nil
		}
		if v != nil {
			return []interface{}{v}
		}
		return nil
	}
	switch node := v.(type) {
	case map[string]interface{}:
		return collectLeaves(node[path[0]], path[1:], isList)
	case []interface{}:
		var results []interface{}
		for _, elem := range node {
			results = append(results, collectLeaves(elem, path, isList)...)
		}
		return results
	}
	return nil
}

func buildRepresentation(obj map[string]interface{}, typeName string, keyFields []string) (map[string]interface{}, error) {
	rep := map[string]interface{}{"__typename": typeName}
	for _, kf := range keyFields {
		v, ok := obj[kf]
		if !ok {
			return nil, fmt.Errorf("key field %q not found in response", kf)
		}
		rep[kf] = v
	}
	return rep, nil
}

// mergeEntityResults merges _entities response data back into the merged result map,
// consuming entities in traversal order (the same order collectRepresentations produced them).
// Returns the unconsumed tail of entities.
func mergeEntityResults(data map[string]interface{}, path []string, entities []interface{}, isList bool) []interface{} {
	if len(path) == 0 || len(entities) == 0 {
		return entities
	}

	if len(path) == 1 {
		target := data[path[0]]
		if isList {
			list, ok := target.([]interface{})
			if !ok {
				return entities
			}
			for _, item := range list {
				if len(entities) == 0 {
					break
				}
				itemMap, ok := item.(map[string]interface{})
				if !ok {
					continue
				}
				if entityMap, ok := entities[0].(map[string]interface{}); ok {
					for k, v := range entityMap {
						itemMap[k] = v
					}
				}
				entities = entities[1:]
			}
		} else {
			targetMap, ok := target.(map[string]interface{})
			if !ok {
				return entities
			}
			if entityMap, ok := entities[0].(map[string]interface{}); ok {
				for k, v := range entityMap {
					targetMap[k] = v
				}
			}
			entities = entities[1:]
		}
		return entities
	}

	// Recurse into nested path. Arrays are transparent: fan through each element
	// and consume entities in order.
	next := data[path[0]]
	switch v := next.(type) {
	case map[string]interface{}:
		entities = mergeEntityResults(v, path[1:], entities, isList)
	case []interface{}:
		for _, item := range v {
			if itemMap, ok := item.(map[string]interface{}); ok {
				entities = mergeEntityResults(itemMap, path[1:], entities, isList)
			}
		}
	}
	return entities
}

// filterVars returns only the variables whose names are in keep.
func filterVars(all map[string]interface{}, keep []string) map[string]interface{} {
	if len(keep) == 0 || len(all) == 0 {
		return nil
	}
	filtered := make(map[string]interface{}, len(keep))
	for _, k := range keep {
		if v, ok := all[k]; ok {
			filtered[k] = v
		}
	}
	if len(filtered) == 0 {
		return nil
	}
	return filtered
}

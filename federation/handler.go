package federation

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// incomingRequest is the parsed GraphQL-over-HTTP request body.
type incomingRequest struct {
	Query         string                 `json:"query"`
	OperationName string                 `json:"operationName"`
	Variables     map[string]interface{} `json:"variables"`
	Extensions    map[string]interface{} `json:"extensions"`
}

// Handler returns an http.Handler that executes GraphQL federation queries
// against the given supergraph routing table using client for subgraph HTTP calls.
func Handler(sg *Supergraph, client *http.Client) http.Handler {
	if client == nil {
		client = http.DefaultClient
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		req, err := parseRequest(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}

		if req.Query == "" {
			writeError(w, http.StatusBadRequest,
				"There was no GraphQL operation to execute. "+
					"Use the `query` parameter to send an operation, using either GET or POST.")
			return
		}

		plan, err := BuildPlan(sg, req.Query, req.OperationName)
		if err != nil {
			writeGraphQLError(w, fmt.Sprintf("query planning: %s", err))
			return
		}

		data, errs, err := Execute(r.Context(), plan, req.Variables, client)
		if err != nil {
			writeGraphQLError(w, fmt.Sprintf("execution: %s", err))
			return
		}

		writeResponse(w, data, errs)
	})
}

func parseRequest(r *http.Request) (*incomingRequest, error) {
	var req incomingRequest

	switch r.Method {
	case http.MethodGet:
		q := r.URL.Query()
		req.Query = q.Get("query")
		req.OperationName = q.Get("operationName")
		if raw := q.Get("variables"); raw != "" {
			if err := json.Unmarshal([]byte(raw), &req.Variables); err != nil {
				return nil, fmt.Errorf("invalid variables: %w", err)
			}
		}
	case http.MethodPost:
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			return nil, fmt.Errorf("invalid JSON body: %w", err)
		}
	default:
		return nil, fmt.Errorf("method not allowed")
	}

	return &req, nil
}

func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"errors": []map[string]string{{"message": msg}},
	})
}

func writeGraphQLError(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK) // GraphQL errors use 200 per spec
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"data":   nil,
		"errors": []map[string]string{{"message": msg}},
	})
}

func writeResponse(w http.ResponseWriter, data map[string]interface{}, errs []GraphQLError) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	resp := map[string]interface{}{"data": data}
	if len(errs) > 0 {
		resp["errors"] = errs
	}
	_ = json.NewEncoder(w).Encode(resp)
}

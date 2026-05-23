// Package health provides liveness and readiness tracking for the router.
package health

import (
	"encoding/json"
	"net/http"
	"sync/atomic"
)

// State tracks liveness and readiness atomically.
type State struct {
	live  atomic.Bool
	ready atomic.Bool
}

// New returns a State with live=true and ready=true.
func New() *State {
	s := &State{}
	s.live.Store(true)
	s.ready.Store(true)
	return s
}

// SetLive sets the liveness state.
func (s *State) SetLive(v bool) { s.live.Store(v) }

// SetReady sets the readiness state.
func (s *State) SetReady(v bool) { s.ready.Store(v) }

// IsLive reports whether the router is live.
func (s *State) IsLive() bool { return s.live.Load() }

// IsReady reports whether the router is ready to serve requests.
func (s *State) IsReady() bool { return s.ready.Load() }

type healthResponse struct {
	Status string `json:"status"`
}

// Handler returns an http.Handler for the health check endpoint.
// Returns {"status":"UP"} with HTTP 200 when healthy,
// {"status":"DOWN"} with HTTP 503 when unhealthy.
func (s *State) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if s.IsLive() && s.IsReady() {
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(healthResponse{Status: "UP"})
		} else {
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(healthResponse{Status: "DOWN"})
		}
	})
}

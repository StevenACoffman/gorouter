package health_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/StevenACoffman/gorouter/internal/health"
)

// TC-02: health check response format
func TestHandler(t *testing.T) {
	tests := []struct {
		name       string
		live       bool
		ready      bool
		wantStatus int
		wantBody   string
	}{
		{
			name:       "healthy returns UP with 200",
			live:       true,
			ready:      true,
			wantStatus: http.StatusOK,
			wantBody:   "UP",
		},
		{
			name:       "not live returns DOWN with 503",
			live:       false,
			ready:      true,
			wantStatus: http.StatusServiceUnavailable,
			wantBody:   "DOWN",
		},
		{
			name:       "not ready returns DOWN with 503",
			live:       true,
			ready:      false,
			wantStatus: http.StatusServiceUnavailable,
			wantBody:   "DOWN",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := health.New()
			s.SetLive(tt.live)
			s.SetReady(tt.ready)

			req := httptest.NewRequest(http.MethodGet, "/health", nil)
			rec := httptest.NewRecorder()
			s.Handler().ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			ct := rec.Header().Get("Content-Type")
			if ct != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", ct)
			}
			var body struct {
				Status string `json:"status"`
			}
			if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
				t.Fatalf("decode body: %v", err)
			}
			if body.Status != tt.wantBody {
				t.Errorf("status field = %q, want %q", body.Status, tt.wantBody)
			}
		})
	}
}

func TestNewDefaultsToHealthy(t *testing.T) {
	s := health.New()
	if !s.IsLive() {
		t.Error("IsLive() = false after New(), want true")
	}
	if !s.IsReady() {
		t.Error("IsReady() = false after New(), want true")
	}
}

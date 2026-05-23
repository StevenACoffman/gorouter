package config_test

import (
	"encoding/json"
	"testing"

	"github.com/StevenACoffman/gorouter/internal/config"
)

func TestDefault(t *testing.T) {
	cfg := config.Default()
	if cfg.Supergraph.Listen != "127.0.0.1:4000" {
		t.Errorf("supergraph.listen = %q, want 127.0.0.1:4000", cfg.Supergraph.Listen)
	}
	if cfg.HealthCheck.Listen != "127.0.0.1:8088" {
		t.Errorf("health_check.listen = %q, want 127.0.0.1:8088", cfg.HealthCheck.Listen)
	}
	if cfg.HealthCheck.Path != "/health" {
		t.Errorf("health_check.path = %q, want /health", cfg.HealthCheck.Path)
	}
	if !cfg.HealthCheck.Enabled {
		t.Error("health_check.enabled = false, want true")
	}
	if cfg.Supergraph.Path != "/" {
		t.Errorf("supergraph.path = %q, want /", cfg.Supergraph.Path)
	}
	if !cfg.APQ.Enabled {
		t.Error("apq.enabled = false, want true")
	}
	if cfg.Batching.Enabled {
		t.Error("batching.enabled = true, want false")
	}
	if cfg.Sandbox.Enabled {
		t.Error("sandbox.enabled = true, want false")
	}
	if !cfg.Homepage.Enabled {
		t.Error("homepage.enabled = false, want true")
	}
}

func TestParse(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr bool
		check   func(t *testing.T, c config.Config)
	}{
		{
			name: "empty yaml uses defaults",
			yaml: "",
			check: func(t *testing.T, c config.Config) {
				t.Helper()
				if c.Supergraph.Listen != "127.0.0.1:4000" {
					t.Errorf("listen = %q, want default 127.0.0.1:4000", c.Supergraph.Listen)
				}
			},
		},
		{
			name: "overrides listen address",
			yaml: "supergraph:\n  listen: \"0.0.0.0:8080\"\n",
			check: func(t *testing.T, c config.Config) {
				t.Helper()
				if c.Supergraph.Listen != "0.0.0.0:8080" {
					t.Errorf("listen = %q, want 0.0.0.0:8080", c.Supergraph.Listen)
				}
			},
		},
		{
			// TC-14: homepage and sandbox cannot both be enabled
			name:    "homepage and sandbox both enabled",
			yaml:    "homepage:\n  enabled: true\nsandbox:\n  enabled: true\n",
			wantErr: true,
		},
		{
			name: "sandbox enabled alone is ok",
			yaml: "homepage:\n  enabled: false\nsandbox:\n  enabled: true\n",
			check: func(t *testing.T, c config.Config) {
				t.Helper()
				if !c.Sandbox.Enabled {
					t.Error("sandbox.enabled = false, want true")
				}
			},
		},
		{
			name:    "invalid yaml",
			yaml:    "supergraph:\n  listen: [not, a, string\n",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := config.Parse([]byte(tt.yaml))
			if (err != nil) != tt.wantErr {
				t.Fatalf("Parse() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err == nil && tt.check != nil {
				tt.check(t, got)
			}
		})
	}
}

// TC-08: config schema subcommand returns valid JSON.
func TestSchemaJSON(t *testing.T) {
	s := config.SchemaJSON()
	if s == "" {
		t.Fatal("SchemaJSON() returned empty string")
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatalf("SchemaJSON() is not valid JSON: %v", err)
	}
	if _, ok := v["$schema"]; !ok {
		t.Error("SchemaJSON() missing $schema field")
	}
	if typ, _ := v["type"].(string); typ != "object" {
		t.Errorf("SchemaJSON() type = %q, want object", typ)
	}
}

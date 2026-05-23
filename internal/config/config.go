// Package config provides router configuration types, parsing, and schema output.
package config

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// Config is the top-level router configuration.
type Config struct {
	Supergraph  Supergraph  `yaml:"supergraph"`
	HealthCheck HealthCheck `yaml:"health_check"`
	Homepage    Homepage    `yaml:"homepage"`
	Sandbox     Sandbox     `yaml:"sandbox"`
	CORS        CORS        `yaml:"cors"`
	APQ         APQ         `yaml:"apq"`
	Batching    Batching    `yaml:"batching"`
}

// Supergraph configures the main GraphQL endpoint.
type Supergraph struct {
	Listen                    string `yaml:"listen"`
	Path                      string `yaml:"path"`
	Introspection             bool   `yaml:"introspection"`
	DeferSupport              bool   `yaml:"defer_support"`
	ConnectionShutdownTimeout string `yaml:"connection_shutdown_timeout"`
}

// HealthCheck configures the health check endpoint.
type HealthCheck struct {
	Listen  string `yaml:"listen"`
	Enabled bool   `yaml:"enabled"`
	Path    string `yaml:"path"`
}

// Homepage configures the landing page served at the GraphQL path.
type Homepage struct {
	Enabled bool `yaml:"enabled"`
}

// Sandbox configures the Apollo Sandbox explorer UI.
type Sandbox struct {
	Enabled bool `yaml:"enabled"`
}

// CORS configures cross-origin resource sharing.
type CORS struct {
	AllowCredentials bool     `yaml:"allow_credentials"`
	AllowMethods     []string `yaml:"allow_methods"`
	ExposeHeaders    []string `yaml:"expose_headers"`
	AllowHeaders     []string `yaml:"allow_headers"`
	AllowOrigins     []string `yaml:"allow_origins"`
}

// APQ configures Automatic Persisted Queries.
type APQ struct {
	Enabled bool `yaml:"enabled"`
}

// Batching configures query batching.
type Batching struct {
	Enabled bool   `yaml:"enabled"`
	Mode    string `yaml:"mode"`
}

// Default returns a Config with all values set to the Apollo Router defaults.
func Default() Config {
	return Config{
		Supergraph: Supergraph{
			Listen:                    "127.0.0.1:4000",
			Path:                      "/",
			DeferSupport:              true,
			ConnectionShutdownTimeout: "60s",
		},
		HealthCheck: HealthCheck{
			Listen:  "127.0.0.1:8088",
			Enabled: true,
			Path:    "/health",
		},
		Homepage: Homepage{Enabled: true},
		Sandbox:  Sandbox{Enabled: false},
		CORS: CORS{
			AllowMethods:  []string{"GET", "POST", "OPTIONS"},
			ExposeHeaders: []string{"Content-Length", "Content-Type"},
		},
		APQ:      APQ{Enabled: true},
		Batching: Batching{Enabled: false, Mode: "batch_http_link"},
	}
}

// Parse parses YAML config bytes into a Config, starting from Default values.
// Returns an error if the YAML is malformed or fails semantic validation.
func Parse(data []byte) (Config, error) {
	cfg := Default()
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("config: parse: %w", err)
	}
	if err := validate(cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func validate(cfg Config) error {
	if cfg.Homepage.Enabled && cfg.Sandbox.Enabled {
		return fmt.Errorf("config: homepage and sandbox cannot both be enabled")
	}
	return nil
}

// SchemaJSON returns a JSON Schema document describing the router configuration.
func SchemaJSON() string {
	return `{
  "$schema": "http://json-schema.org/draft-07/schema#",
  "title": "Apollo Router Configuration",
  "type": "object",
  "properties": {
    "supergraph": {
      "type": "object",
      "description": "GraphQL endpoint configuration",
      "properties": {
        "listen": {
          "type": "string",
          "default": "127.0.0.1:4000",
          "description": "Address and port to listen on"
        },
        "path": {
          "type": "string",
          "default": "/",
          "description": "URL path for the GraphQL endpoint"
        },
        "introspection": {
          "type": "boolean",
          "default": false,
          "description": "Enable GraphQL introspection"
        },
        "defer_support": {
          "type": "boolean",
          "default": true,
          "description": "Enable @defer directive support"
        },
        "connection_shutdown_timeout": {
          "type": "string",
          "default": "60s",
          "description": "Time to wait for connections to close on shutdown"
        }
      }
    },
    "health_check": {
      "type": "object",
      "description": "Health check endpoint configuration",
      "properties": {
        "listen": {
          "type": "string",
          "default": "127.0.0.1:8088",
          "description": "Address and port for the health check server"
        },
        "enabled": {
          "type": "boolean",
          "default": true
        },
        "path": {
          "type": "string",
          "default": "/health"
        }
      }
    },
    "homepage": {
      "type": "object",
      "properties": {
        "enabled": { "type": "boolean", "default": true }
      }
    },
    "sandbox": {
      "type": "object",
      "description": "Apollo Sandbox explorer UI (mutually exclusive with homepage)",
      "properties": {
        "enabled": { "type": "boolean", "default": false }
      }
    },
    "cors": {
      "type": "object",
      "properties": {
        "allow_credentials": { "type": "boolean", "default": false },
        "allow_methods": {
          "type": "array",
          "items": { "type": "string" },
          "default": ["GET", "POST", "OPTIONS"]
        },
        "expose_headers": {
          "type": "array",
          "items": { "type": "string" },
          "default": ["Content-Length", "Content-Type"]
        },
        "allow_headers": {
          "type": "array",
          "items": { "type": "string" }
        },
        "allow_origins": {
          "type": "array",
          "items": { "type": "string" }
        }
      }
    },
    "apq": {
      "type": "object",
      "description": "Automatic Persisted Queries",
      "properties": {
        "enabled": { "type": "boolean", "default": true }
      }
    },
    "batching": {
      "type": "object",
      "properties": {
        "enabled": { "type": "boolean", "default": false },
        "mode": { "type": "string", "default": "batch_http_link" }
      }
    }
  },
  "additionalProperties": false
}`
}

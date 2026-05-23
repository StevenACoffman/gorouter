// Package run implements the "run" CLI command that starts the router HTTP server.
package run

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/peterbourgon/ff/v4"

	"github.com/StevenACoffman/gorouter/cmd/root"
	"github.com/StevenACoffman/gorouter/internal/config"
	"github.com/StevenACoffman/gorouter/federation"
	"github.com/StevenACoffman/gorouter/internal/health"
	"github.com/StevenACoffman/gorouter/internal/server"
)

// Config holds the run subcommand's parsed flags.
type Config struct {
	*root.Config
	ConfigPath     string
	SupergraphPath string
	ListenOverride string
	Dev            bool
	HotReload      bool
	LogLevel       string
	Flags          *ff.FlagSet
	Command        *ff.Command
}

// New creates and registers the run command with the given parent config.
func New(parent *root.Config) *Config {
	var cfg Config
	cfg.Config = parent
	cfg.Flags = ff.NewFlagSet("run").SetParent(parent.Flags)
	cfg.Flags.StringVar(&cfg.ConfigPath, 'c', "config", "", "path to router configuration YAML file")
	cfg.Flags.StringVar(&cfg.SupergraphPath, 's', "supergraph", "", "path to supergraph SDL file")
	cfg.Flags.StringVar(&cfg.ListenOverride, 0, "listen", "", "override supergraph.listen address")
	cfg.Flags.BoolVar(&cfg.Dev, 0, "dev", "enable development mode")
	cfg.Flags.BoolVar(&cfg.HotReload, 0, "hot-reload", "reload config and schema on file changes")
	cfg.Flags.StringVar(&cfg.LogLevel, 'l', "log", "info", "log level: off error warn info debug trace")
	cfg.Command = &ff.Command{
		Name:      "run",
		Usage:     "gorouter run [FLAGS]",
		ShortHelp: "start the router HTTP server",
		LongHelp: `Start the Apollo Router HTTP server.

The router listens on two ports:
  - GraphQL endpoint (default: 127.0.0.1:4000)
  - Health check endpoint (default: 127.0.0.1:8088)

Every flag can be set via an APOLLO_ROUTER_-prefixed environment variable.
The mapping rule is: prepend APOLLO_ROUTER_, uppercase, replace dashes with underscores.
Example: --config -> APOLLO_ROUTER_CONFIG

Provide --supergraph to enable federation query planning. Without it the server
starts but returns an informational error for all GraphQL requests.`,
		Flags: cfg.Flags,
		Exec:  cfg.exec,
	}
	parent.Command.Subcommands = append(parent.Command.Subcommands, cfg.Command)
	return &cfg
}

func (cfg *Config) exec(ctx context.Context, _ []string) error {
	routerCfg, err := cfg.loadConfig()
	if err != nil {
		return err
	}
	if cfg.ListenOverride != "" {
		routerCfg.Supergraph.Listen = cfg.ListenOverride
	}

	hs := health.New()
	gqlHandler, err := cfg.buildGraphQLHandler()
	if err != nil {
		return err
	}

	srvCfg := server.Config{
		GraphQLAddr:     routerCfg.Supergraph.Listen,
		GraphQLPath:     routerCfg.Supergraph.Path,
		HealthAddr:      routerCfg.HealthCheck.Listen,
		HealthPath:      routerCfg.HealthCheck.Path,
		ShutdownTimeout: 60 * time.Second,
	}

	_, _ = fmt.Fprintf(cfg.Stdout, "GraphQL endpoint: http://%s%s\n",
		srvCfg.GraphQLAddr, srvCfg.GraphQLPath)
	_, _ = fmt.Fprintf(cfg.Stdout, "Health endpoint:  http://%s%s\n",
		srvCfg.HealthAddr, srvCfg.HealthPath)

	return server.Serve(ctx, srvCfg, gqlHandler, hs.Handler())
}

// buildGraphQLHandler returns the federation handler when a supergraph SDL is provided,
// or a stub handler that explains what's missing when it is not.
func (cfg *Config) buildGraphQLHandler() (http.Handler, error) {
	sdlPath := cfg.SupergraphPath
	if sdlPath == "" {
		sdlPath = cfg.Config.Getenv("APOLLO_ROUTER_SUPERGRAPH_PATH")
	}
	if sdlPath == "" {
		return stubHandler("no supergraph SDL provided; start with --supergraph <path>"), nil
	}

	sdl, err := os.ReadFile(sdlPath)
	if err != nil {
		return nil, fmt.Errorf("run: read supergraph: %w", err)
	}

	sg, err := federation.ParseSchema(string(sdl))
	if err != nil {
		return nil, fmt.Errorf("run: parse supergraph: %w", err)
	}

	_, _ = fmt.Fprintf(cfg.Stdout, "Supergraph:       %s\n", sdlPath)
	return federation.Handler(sg, nil), nil
}

func (cfg *Config) loadConfig() (config.Config, error) {
	if cfg.ConfigPath == "" {
		return config.Default(), nil
	}
	data, err := os.ReadFile(cfg.ConfigPath)
	if err != nil {
		return config.Config{}, fmt.Errorf("run: read config: %w", err)
	}
	c, err := config.Parse(data)
	if err != nil {
		return config.Config{}, fmt.Errorf("run: %w", err)
	}
	return c, nil
}

// stubHandler returns a handler that returns an informational GraphQL error.
func stubHandler(msg string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data":   nil,
			"errors": []map[string]string{{"message": msg}},
		})
	})
}

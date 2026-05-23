// Package schema implements the "config schema" CLI subcommand.
package schema

import (
	"context"
	"fmt"

	"github.com/peterbourgon/ff/v4"

	"github.com/StevenACoffman/gorouter/cmd/root"
	"github.com/StevenACoffman/gorouter/internal/config"
)

// Config holds the config schema subcommand's state.
type Config struct {
	*root.Config
	Flags   *ff.FlagSet
	Command *ff.Command
}

// New creates and registers the config schema subcommand under parentCmd.
// parentFlags is used for flag inheritance; parentCmd is where the subcommand is registered.
func New(rootCfg *root.Config, parentFlags *ff.FlagSet, parentCmd *ff.Command) *Config {
	var cfg Config
	cfg.Config = rootCfg
	cfg.Flags = ff.NewFlagSet("schema").SetParent(parentFlags)
	cfg.Command = &ff.Command{
		Name:      "schema",
		Usage:     "gorouter config schema",
		ShortHelp: "print the router configuration JSON schema",
		Flags:     cfg.Flags,
		Exec:      cfg.exec,
	}
	parentCmd.Subcommands = append(parentCmd.Subcommands, cfg.Command)
	return &cfg
}

func (cfg *Config) exec(_ context.Context, _ []string) error {
	_, _ = fmt.Fprintln(cfg.Stdout, config.SchemaJSON())
	return nil
}

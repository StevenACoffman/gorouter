// Package config implements the "config" CLI command group.
package config

import (
	"github.com/peterbourgon/ff/v4"

	"github.com/StevenACoffman/gorouter/cmd/config/schema"
	"github.com/StevenACoffman/gorouter/cmd/config/validate"
	"github.com/StevenACoffman/gorouter/cmd/root"
)

// Config holds the config command group's state.
type Config struct {
	*root.Config
	Flags   *ff.FlagSet
	Command *ff.Command
}

// New creates the config command group and registers schema and validate subcommands under it.
// Exec is nil — invoking "gorouter config" without a subcommand returns ff.ErrNoExec,
// which run() in main.go treats as success (exits 0).
func New(parent *root.Config) *Config {
	var cfg Config
	cfg.Config = parent
	cfg.Flags = ff.NewFlagSet("config").SetParent(parent.Flags)
	cfg.Command = &ff.Command{
		Name:      "config",
		Usage:     "gorouter config <SUBCOMMAND>",
		ShortHelp: "validate, inspect, and upgrade router configuration",
		Flags:     cfg.Flags,
	}
	parent.Command.Subcommands = append(parent.Command.Subcommands, cfg.Command)

	// Register subcommands. schema and validate do not import this package,
	// so there is no import cycle.
	schema.New(parent, cfg.Flags, cfg.Command)
	validate.New(parent, cfg.Flags, cfg.Command)
	return &cfg
}

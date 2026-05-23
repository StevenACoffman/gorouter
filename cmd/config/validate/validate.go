// Package validate implements the "config validate" CLI subcommand.
package validate

import (
	"context"
	"fmt"
	"os"

	"github.com/peterbourgon/ff/v4"

	"github.com/StevenACoffman/gorouter/cmd/root"
	"github.com/StevenACoffman/gorouter/internal/config"
)

// Config holds the config validate subcommand's state.
type Config struct {
	*root.Config
	Flags   *ff.FlagSet
	Command *ff.Command
}

// New creates and registers the config validate subcommand under parentCmd.
// parentFlags is used for flag inheritance; parentCmd is where the subcommand is registered.
func New(rootCfg *root.Config, parentFlags *ff.FlagSet, parentCmd *ff.Command) *Config {
	var cfg Config
	cfg.Config = rootCfg
	cfg.Flags = ff.NewFlagSet("validate").SetParent(parentFlags)
	cfg.Command = &ff.Command{
		Name:      "validate",
		Usage:     "gorouter config validate <CONFIG-FILE>",
		ShortHelp: "validate a router configuration file",
		LongHelp: `Validate a router YAML configuration file.

Exits 0 if the file is valid; exits 1 and prints errors to stderr if not.`,
		Flags: cfg.Flags,
		Exec:  cfg.exec,
	}
	parentCmd.Subcommands = append(parentCmd.Subcommands, cfg.Command)
	return &cfg
}

func (cfg *Config) exec(_ context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("validate: missing required argument: <CONFIG-FILE>")
	}
	path := args[0]
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("validate: read %s: %w", path, err)
	}
	if _, err := config.Parse(data); err != nil {
		_, _ = fmt.Fprintln(cfg.Stderr, err.Error())
		return root.ExitError(1)
	}
	_, _ = fmt.Fprintf(cfg.Stdout, "config %s is valid\n", path)
	return nil
}

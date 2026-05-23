// Package cmd is the dispatcher for the gorouter CLI.
// It registers all commands and routes incoming arguments
// to the matching command implementation.
package cmd

// climax:name gorouter
// climax:root-pkg root
// climax:env-prefix APOLLO_ROUTER

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/peterbourgon/ff/v4"
	"github.com/peterbourgon/ff/v4/ffhelp"

	routercfg "github.com/StevenACoffman/gorouter/cmd/config"
	"github.com/StevenACoffman/gorouter/cmd/root"
	"github.com/StevenACoffman/gorouter/cmd/run"
	"github.com/StevenACoffman/gorouter/cmd/version"
	// climax:imports
)

// Run parses args and dispatches to the matching command.
// args must not include the executable name (pass os.Args[1:]).
//
// Every flag can be set via an APOLLO_ROUTER_-prefixed environment variable.
// The mapping rule is: prepend APOLLO_ROUTER_, uppercase, replace dashes with
// underscores.
//
// Flags supplied on the command line always take precedence over env vars.
func Run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	r := root.New(stdin, stdout, stderr)
	version.New(r)
	run.New(r)
	routercfg.New(r)
	// register new commands here

	if err := r.Command.Parse(args, ff.WithEnvVarPrefix("APOLLO_ROUTER")); err != nil {
		_, _ = fmt.Fprintf(stderr, "\n%s\n", ffhelp.Command(r.Command))
		return fmt.Errorf("parse: %w", err)
	}

	if err := r.Command.Run(ctx); err != nil {
		// Don't print usage help for ErrNoExec (no subcommand given) or
		// ExitError (command already reported its own outcome).
		var exitErr root.ExitError
		if !errors.Is(err, ff.ErrNoExec) && !errors.As(err, &exitErr) {
			_, _ = fmt.Fprintf(stderr, "\n%s\n", ffhelp.Command(r.Command.GetSelected()))
		}
		return err
	}

	return nil
}

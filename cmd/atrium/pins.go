package main

import (
	"fmt"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	dbkey "github.com/jeiang/atrium/internal/db/key"
	"github.com/jeiang/atrium/internal/settings"
)

func pinned(cmds []*cli.Command) []*cli.Command {
	for _, c := range cmds {
		if c.Setup != nil {
			setup := c.Setup
			c.Setup = func(a *cli.Args) cli.Handler {
				run := setup(a)
				return func(e *cli.Env) int {
					pins, err := config.LoadPins(config.OS, settings.Default)
					if err != nil {
						_, _ = fmt.Fprintf(e.Stderr, "error: %v\n", err)
						return cli.ExitError
					}
					settings.Default.SetPins(pins)
					if err := dbkey.Configure(config.OS, settings.Default); err != nil {
						_, _ = fmt.Fprintf(e.Stderr, "error: %v\n", err)
						return cli.ExitError
					}
					return run(e)
				}
			}
		}
		pinned(c.Commands)
	}
	return cmds
}

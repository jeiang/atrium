package main

import (
	"fmt"

	"github.com/jeiang/atrium/internal/cli"
)

const codemodeRemoved = "atrium codemode-worker: removed in v2; the inert atrium-codemode@ unit still names this command"

// codemodeWorkerCommand is `atrium codemode-worker`, which the host's atrium-codemode@ unit runs as its
// ExecStart: code mode is gone, so the command only reports that and fails.
func codemodeWorkerCommand() *cli.Command {
	return &cli.Command{
		Name: "codemode-worker",
		Setup: func(*cli.Args) cli.Handler {
			return func(env *cli.Env) int {
				_, _ = fmt.Fprintln(env.Stderr, codemodeRemoved)
				return cli.ExitError
			}
		},
	}
}

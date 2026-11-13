package agentcmd

import (
	"context"
	"errors"
	"fmt"
	"time"

	asstapp "github.com/jeiang/atrium/internal/assistant/application"
	"github.com/jeiang/atrium/internal/cli"
)

func tickCommand(tick Tick, enabled func(string) bool) *cli.Command {
	return &cli.Command{Name: "tick", Setup: func(*cli.Args) cli.Handler {
		return func(env *cli.Env) int {
			ctx, cancel := context.WithTimeout(env.Ctx, 2*time.Hour)
			defer cancel()
			err := tick(ctx, enabled)
			if errors.Is(err, asstapp.ErrAllBusy) {
				_, _ = fmt.Fprintf(env.Stderr, "%v\n", err)
				return cli.ExitTempFail
			}
			if err != nil {
				return cli.Fail(env, err)
			}
			return cli.ExitOK
		}
	}}
}

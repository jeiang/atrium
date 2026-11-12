package agentcmd

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jeiang/atrium/internal/assistant/jobworker"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/scheduler"
)

func tickCommand(open Open, enabled func(string) bool) *cli.Command {
	return &cli.Command{Name: "tick", Setup: func(*cli.Args) cli.Handler {
		return func(env *cli.Env) int {
			ctx, cancel := context.WithTimeout(env.Ctx, 2*time.Hour)
			defer cancel()
			ctx, svc, closeApp, err := open(ctx)
			if err == nil {
				defer closeApp()
				s := jobworker.New(svc, module.SetFrom(ctx).Catalog().Jobs(), jobworker.Options{Enabled: enabled})
				err = s.Once(ctx)
			}
			if errors.Is(err, scheduler.ErrAllBusy) {
				_, _ = fmt.Fprintf(env.Stderr, "%v\n", err)
				return cli.ExitTempFail
			}
			if err != nil {
				_, _ = fmt.Fprintf(env.Stderr, "error: %v\n", err)
				return cli.ExitError
			}
			return cli.ExitOK
		}
	}}
}

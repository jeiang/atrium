package agentcmd

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jeiang/atrium/internal/assistant/jobworker"
	"github.com/jeiang/atrium/internal/assistant/registry"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/scheduler"
)

func tickCommand(cfgEnv config.Env, tools registry.Source, enabled func(string) bool, holder *jobworker.Holder) *cli.Command {
	return &cli.Command{Name: "tick", Setup: func(*cli.Args) cli.Handler {
		return func(env *cli.Env) int {
			ctx, cancel := context.WithTimeout(env.Ctx, 2*time.Hour)
			defer cancel()
			svc, closeApp, err := bootstrap(ctx, cfgEnv, tools, nil)
			if err == nil {
				defer closeApp()
				if holder != nil {
					holder.Set(svc)
				}
				s := jobworker.New(svc, module.SetFrom(ctx).Catalog().Jobs(), enabled, nil)
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

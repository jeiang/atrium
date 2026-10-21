package calendarcmd

import (
	"context"
	"fmt"

	"github.com/jeiang/atrium/internal/assistant/registry"
	"github.com/jeiang/atrium/internal/assistant/runner"
	"github.com/jeiang/atrium/internal/assistant/scheduler"
	"github.com/jeiang/atrium/internal/assistant/scheduler/worker"
	"github.com/jeiang/atrium/internal/assistant/service"
	"github.com/jeiang/atrium/internal/calendar"
	"github.com/jeiang/atrium/internal/calendar/candidates"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/py"
)

func Callables() map[string]worker.Callable {
	return map[string]worker.Callable{
		scheduler.CalendarCandidatesName: func(ctx context.Context, e worker.Env) (worker.Result, error) {
			_, limit, err := calendar.CandidateLimits(ctx, e.Service.DB.Read)
			if err != nil {
				return worker.Result{}, err
			}
			out, err := candidates.DraftEventProposals(ctx, e.Service, limit, e.Now)
			return worker.Result{RunID: out.RunID}, err
		},
	}
}

func candidatesCommand(env config.Env, tools registry.Source, now func() float64) *cli.Command {
	return &cli.Command{
		Name: "candidates",
		Setup: func(*cli.Args) cli.Handler {
			return func(e *cli.Env) int {
				cfg, err := config.Load(env)
				if err != nil {
					return cli.Fail(e, err)
				}
				d, err := db.Connect(cfg.DBPath)
				if err != nil {
					return cli.Fail(e, err)
				}
				defer func() { _ = d.Close() }()
				if err := config.Migrate(e.Ctx, d, cfg); err != nil {
					return cli.Fail(e, err)
				}
				r, err := runner.New(tools)
				if err != nil {
					return cli.Fail(e, err)
				}
				svc := service.New(e.Ctx, d, cfg, env, r)
				defer svc.Close()
				_, limit, err := calendar.CandidateLimits(e.Ctx, d.Read)
				if err != nil {
					return cli.Fail(e, err)
				}
				out, err := candidates.DraftEventProposals(e.Ctx, svc, limit, now)
				if err != nil {
					return cli.Fail(e, err)
				}
				_, _ = fmt.Fprintln(e.Stdout, py.JSONDumps(out.Dict(), py.JSONOpts{Indent: 2}))
				return cli.ExitOK
			}
		},
	}
}

package calendarcmd

import (
	"context"
	"fmt"

	"github.com/jeiang/atrium/internal/assistant/runner"
	"github.com/jeiang/atrium/internal/assistant/scheduler"
	"github.com/jeiang/atrium/internal/assistant/scheduler/worker"
	"github.com/jeiang/atrium/internal/assistant/service"
	"github.com/jeiang/atrium/internal/calendar/candidates"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/py"
)

func Callables() map[string]worker.Callable {
	return map[string]worker.Callable{
		scheduler.CalendarCandidatesName: func(ctx context.Context, e worker.Env) (worker.Result, error) {
			out, err := candidates.DraftEventProposals(ctx, e.Service, candidates.DraftLimit, e.Now)
			return worker.Result{RunID: out.RunID}, err
		},
	}
}

func candidatesCommand(env config.Env, now func() float64) *cli.Command {
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
				if err := db.Migrate(e.Ctx, d, cfg.DBPath); err != nil {
					return cli.Fail(e, err)
				}
				r, err := runner.New()
				if err != nil {
					return cli.Fail(e, err)
				}
				svc := service.New(e.Ctx, d, cfg, env, r)
				defer svc.Close()
				out, err := candidates.DraftEventProposals(e.Ctx, svc, candidates.DraftLimit, now)
				if err != nil {
					return cli.Fail(e, err)
				}
				_, _ = fmt.Fprintln(e.Stdout, py.JSONDumps(out.Dict(), py.JSONOpts{Indent: 2}))
				return cli.ExitOK
			}
		},
	}
}

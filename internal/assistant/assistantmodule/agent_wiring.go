package assistantmodule

import (
	"context"

	"github.com/jeiang/atrium/internal/assistant/agentcmd"
	asstapp "github.com/jeiang/atrium/internal/assistant/application"
	"github.com/jeiang/atrium/internal/assistant/jobworker"
	"github.com/jeiang/atrium/internal/assistant/service"
	"github.com/jeiang/atrium/internal/module"
)

type serviceOpen func(context.Context) (context.Context, *service.Service, func(), error)

func agentOpen(open serviceOpen) agentcmd.Open {
	return func(ctx context.Context) (context.Context, *asstapp.Service, func(), error) {
		ctx, svc, done, err := open(ctx)
		if err != nil {
			return nil, nil, nil, err
		}
		return ctx, asstapp.New(svc.DB, svc.Env, svc.Now).WithRuns(func() *service.Service { return svc }), done, nil
	}
}

func agentTick(open serviceOpen) agentcmd.Tick {
	return func(ctx context.Context, enabled func(string) bool) error {
		ctx, svc, done, err := open(ctx)
		if err != nil {
			return err
		}
		defer done()
		return jobworker.New(svc, module.SetFrom(ctx).Catalog().Jobs(), jobworker.Options{Enabled: enabled, Start: asstapp.ScheduledStart}).Once(ctx)
	}
}

package memorycmd

import (
	"context"

	"github.com/jeiang/atrium/internal/assistant/memory"
	"github.com/jeiang/atrium/internal/assistant/scheduler"
	"github.com/jeiang/atrium/internal/assistant/scheduler/worker"
	"github.com/jeiang/atrium/internal/module"
)

func Callables() map[string]worker.Callable {
	return map[string]worker.Callable{
		scheduler.MemoryWorkName: {
			Modules: []string{module.Assistant},
			Run: func(ctx context.Context, e worker.Env) (worker.Result, error) {
				client, err := e.Service.ResolveClient(ctx)
				if err != nil {
					return worker.Result{}, err
				}
				model, err := memory.Model(ctx, e.Service.DB.Read)
				if err != nil {
					return worker.Result{}, err
				}
				effort, err := memory.Effort(ctx, e.Service.DB.Read)
				if err != nil {
					return worker.Result{}, err
				}
				found, err := memory.QueuedThreads(ctx, e.Service.DB.Read, e.Now())
				if err != nil {
					return worker.Result{}, err
				}
				for _, threadID := range found {
					if _, err := memory.Work(ctx, e.Service.DB, threadID, memory.WorkOptions{
						Now: e.Now(), Client: client, Model: model, Effort: effort,
					}); err != nil {
						return worker.Result{}, err
					}
				}
				return worker.Result{}, nil
			},
		},
	}
}

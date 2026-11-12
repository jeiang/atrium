package memorycmd

import (
	"context"
	"time"

	"github.com/jeiang/atrium/internal/assistant/memory"
	"github.com/jeiang/atrium/internal/assistant/scheduler"
	"github.com/jeiang/atrium/internal/assistant/scheduler/worker"
	"github.com/jeiang/atrium/internal/module"
	scheddef "github.com/jeiang/atrium/internal/scheduler"
)

func Job(deps worker.Dependencies) scheddef.Definition {
	return scheddef.Definition{
		Module: module.Assistant, Key: module.Assistant + "/" + scheduler.MemoryWorkName, Name: scheduler.MemoryWorkName,
		Kind: scheduler.JobBuiltin, Prompt: "Every 2 minutes: summarize pending memory nodes (no model run when the queue is empty).",
		Schedule: "*/2 * * * *", ScheduleSetting: scheduler.KeyMemoryWork, Timeout: 15 * time.Minute,
		Spend: scheddef.ConditionalSpends, Needs: []string{module.Assistant},
		Run: deps.Callable(func(ctx context.Context, e worker.Env) (worker.Result, error) {
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
					Now: e.Now(), Client: client, Inference: e.Service.Inference, Model: model, Effort: effort,
				}); err != nil {
					return worker.Result{}, err
				}
			}
			return worker.Result{}, nil
		}),
	}
}

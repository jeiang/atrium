package memorycmd

import (
	"context"
	"time"

	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/assistant/jobworker"
	"github.com/jeiang/atrium/internal/assistant/memory"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/scheduler"
)

func Job(deps jobworker.Dependencies) scheduler.Definition {
	return scheduler.Definition{
		Module: module.Assistant, Key: module.Assistant + "/" + assistant.MemoryWorkName, Name: assistant.MemoryWorkName,
		Kind: scheduler.JobBuiltin, Prompt: "Every 2 minutes: summarize pending memory nodes (no model run when the queue is empty).",
		Schedule: "*/2 * * * *", ScheduleSetting: assistant.KeyMemoryWork, Timeout: 15 * time.Minute,
		Spend: scheduler.ConditionalSpends, Needs: []string{module.Assistant},
		Run: deps.Callable(func(ctx context.Context, e jobworker.Env) (jobworker.Result, error) {
			client, err := e.Service.ResolveClient(ctx)
			if err != nil {
				return jobworker.Result{}, err
			}
			model, err := memory.Model(ctx, e.Service.DB.Read)
			if err != nil {
				return jobworker.Result{}, err
			}
			effort, err := memory.Effort(ctx, e.Service.DB.Read)
			if err != nil {
				return jobworker.Result{}, err
			}
			found, err := memory.QueuedThreads(ctx, e.Service.DB.Read, e.Now())
			if err != nil {
				return jobworker.Result{}, err
			}
			for _, threadID := range found {
				if _, err := memory.Work(ctx, e.Service.DB, threadID, memory.WorkOptions{
					Now: e.Now(), Client: client, Inference: e.Service.Inference, Model: model, Effort: effort,
				}); err != nil {
					return jobworker.Result{}, err
				}
			}
			return jobworker.Result{}, nil
		}),
	}
}

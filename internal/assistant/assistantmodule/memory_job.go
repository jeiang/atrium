package assistantmodule

import (
	"context"
	"time"

	"github.com/jeiang/atrium/internal/assistant"
	asstapp "github.com/jeiang/atrium/internal/assistant/application"
	"github.com/jeiang/atrium/internal/assistant/jobworker"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/scheduler"
)

func memoryWorkJob(deps jobworker.Dependencies) scheduler.Definition {
	return scheduler.Definition{
		Module: module.Assistant, Key: module.Assistant + "/" + assistant.MemoryWorkName, Name: assistant.MemoryWorkName,
		Kind: scheduler.JobBuiltin, Prompt: "Every 2 minutes: summarize pending memory nodes (no model run when the queue is empty).",
		Schedule: "*/2 * * * *", ScheduleSetting: assistant.KeyMemoryWork, Persistent: true, Timeout: 15 * time.Minute,
		Spend: scheduler.ConditionalSpends, Needs: []string{module.Assistant},
		Run: deps.Callable(func(ctx context.Context, e jobworker.Env) (jobworker.Result, error) {
			svc := asstapp.New(e.Service.DB, e.Service.Env, e.Now).WithMemoryModel(e.Service.ResolveClient, e.Service.Inference)
			_, err := svc.MemoryWork(ctx, asstapp.MemoryWorkInput{Queued: true})
			return jobworker.Result{}, err
		}),
	}
}

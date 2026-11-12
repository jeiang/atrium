package export

import (
	"context"
	"time"

	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/assistant/jobworker"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/scheduler"
)

func Job(deps jobworker.Dependencies) scheduler.Definition {
	return scheduler.Definition{
		Module: module.Assistant, Key: module.Assistant + "/" + assistant.KnowledgeExportName, Name: assistant.KnowledgeExportName,
		Kind: scheduler.JobBuiltin, Prompt: "Daily: project the knowledge base into the configured export targets (no model run).",
		Schedule: "daily 03:30", ScheduleSetting: assistant.KeyKnowledgeExport, Timeout: 30 * time.Minute,
		Spend: scheduler.ModelFree, Needs: []string{module.Assistant},
		Run: deps.Callable(func(ctx context.Context, e jobworker.Env) (jobworker.Result, error) {
			_, err := Run(ctx, e.Service.DB, e.Service.Env, e.Now())
			return jobworker.Result{}, err
		}),
	}
}

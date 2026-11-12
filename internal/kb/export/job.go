package export

import (
	"context"
	"time"

	"github.com/jeiang/atrium/internal/assistant/scheduler"
	"github.com/jeiang/atrium/internal/assistant/scheduler/worker"
	"github.com/jeiang/atrium/internal/module"
	scheddef "github.com/jeiang/atrium/internal/scheduler"
)

func Job(deps worker.Dependencies) scheddef.Definition {
	return scheddef.Definition{
		Module: module.Assistant, Key: module.Assistant + "/" + scheduler.KnowledgeExportName, Name: scheduler.KnowledgeExportName,
		Kind: scheduler.JobBuiltin, Prompt: "Daily: project the knowledge base into the configured export targets (no model run).",
		Schedule: "daily 03:30", ScheduleSetting: scheduler.KeyKnowledgeExport, Timeout: 30 * time.Minute,
		Spend: scheddef.ModelFree, Needs: []string{module.Assistant},
		Run: deps.Callable(func(ctx context.Context, e worker.Env) (worker.Result, error) {
			_, err := Run(ctx, e.Service.DB, e.Service.Env, e.Now())
			return worker.Result{}, err
		}),
	}
}

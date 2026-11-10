package export

import (
	"context"

	"github.com/jeiang/atrium/internal/assistant/scheduler"
	"github.com/jeiang/atrium/internal/assistant/scheduler/worker"
	"github.com/jeiang/atrium/internal/module"
)

func Callables() map[string]worker.Callable {
	return map[string]worker.Callable{
		scheduler.KnowledgeExportName: {
			Modules: []string{module.Assistant},
			Run: func(ctx context.Context, e worker.Env) (worker.Result, error) {
				_, err := Run(ctx, e.Service.DB, e.Service.Env, e.Now())
				return worker.Result{}, err
			},
		},
	}
}

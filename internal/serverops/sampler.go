package serverops

import (
	"context"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/jeiang/atrium/internal/assistant/jobworker"
	grafanatools "github.com/jeiang/atrium/internal/assistant/tools/grafana"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/scheduler"
)

func SamplerJob(deps jobworker.Dependencies, sample func(context.Context, jobworker.Env) error) scheduler.Definition {
	return scheduler.Definition{
		Module: module.Grafana, Key: module.Grafana + "/" + grafanatools.JobSampler, Name: grafanatools.JobSampler, Kind: scheduler.JobBuiltin,
		Prompt:   "Every 15 minutes: sample host metrics and firing alerts into ops samples (no model run).",
		Schedule: "*/15 * * * *", ScheduleSetting: grafanatools.KeyScheduleSampler, Persistent: true, Timeout: 5 * time.Minute,
		Spend: scheduler.ModelFree, Needs: []string{module.Grafana, module.Assistant},
		Run: deps.Callable(func(ctx context.Context, e jobworker.Env) (jobworker.Result, error) {
			if err := sample(ctx, e); err != nil {
				return jobworker.Result{}, err
			}
			return jobworker.Result{}, Supervise(ctx, e)
		}),
	}
}

func SplitHosts(value string) []string {
	var out []string
	for _, host := range strings.FieldsFunc(value, func(r rune) bool { return r == ',' || unicode.IsSpace(r) }) {
		if host != "" && !slices.Contains(out, host) {
			out = append(out, host)
		}
	}
	return out
}

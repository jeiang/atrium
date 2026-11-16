package davmirror

import (
	"context"
	"time"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/runtime"
	"github.com/jeiang/atrium/internal/scheduler"
)

type Prepare func(ctx context.Context, d *db.DB) (func(context.Context) (Stats, error), error)

func Pass(ctx context.Context, d *db.DB, prepare Prepare) (Stats, error) {
	run, err := prepare(ctx, d)
	if err != nil {
		return Stats{}, err
	}
	return run(ctx)
}

func Job(owner, pass, prompt string, run func(context.Context, scheduler.Run) (scheduler.Result, error)) scheduler.Definition {
	return scheduler.Definition{
		Module: owner, Key: owner + "/" + pass, Name: pass, Kind: scheduler.JobBuiltin, Prompt: prompt,
		Every: 15 * time.Minute, InitialDelay: time.Minute, Timeout: 10 * time.Minute, Spend: scheduler.ModelFree,
		Backoff:  runtime.Backoff{Initial: time.Minute, Maximum: 15 * time.Minute},
		Shutdown: scheduler.ShutdownCancel, RunsWhilePaused: true, Resources: []string{pass}, Needs: []string{owner}, Run: run,
	}
}

package openloops

import (
	"context"
	"errors"
	"fmt"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/logx"
)

type Options struct {
	Env      config.Env
	Judge    Judge
	Clock    func() float64
	CacheDir string
}

type RefreshResult struct {
	Loops       any `json:"loops"`
	Commitments any `json:"commitments"`
	Reminders   any `json:"reminders"`
}

func Refresh(ctx context.Context, d *db.DB, now float64, o Options) (RefreshResult, error) {
	env := o.Env
	if env == nil {
		env = config.OS
	}
	reminders := &Reminders{DB: d, Now: o.Clock, Env: env}
	steps := []struct {
		name string
		run  func() (any, error)
	}{
		{"loops", func() (any, error) {
			return Compute(ctx, d, o.CacheDir, now)
		}},
		{"commitments", func() (any, error) { return ScanCommitments(ctx, d, o.CacheDir, env, now, o.Judge) }},
		{"reminders", func() (any, error) { return reminders.Propose(ctx, now, config.From(ctx).TZName(env)) }},
	}
	var out RefreshResult
	var errs []error
	for _, step := range steps {
		result, err := step.run()
		if err != nil {
			log.Error(fmt.Sprintf("open loops: %s failed", step.name), logx.Err, err.Error())
			errs = append(errs, fmt.Errorf("%s: %w", step.name, err))
			result = nil
		}
		switch step.name {
		case "loops":
			out.Loops = result
		case "commitments":
			out.Commitments = result
		case "reminders":
			out.Reminders = result
		}
	}
	return out, errors.Join(errs...)
}

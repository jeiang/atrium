package openloops

import (
	"context"
	"errors"
	"fmt"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/py"
)

type Options struct {
	Env   config.Env
	Judge Judge
	Clock func() float64
}

func Refresh(ctx context.Context, d *db.DB, now float64, o Options) (py.Dict, error) {
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
			counts, err := Compute(ctx, d, now)
			return counts.Dict(), err
		}},
		{"commitments", func() (any, error) { return ScanCommitments(ctx, d, env, now, o.Judge) }},
		{"reminders", func() (any, error) { return reminders.Propose(ctx, now, config.TZName(env)) }},
	}
	out := make(py.Dict, 0, len(steps))
	var errs []error
	for _, step := range steps {
		result, err := step.run()
		if err != nil {
			log.Error(fmt.Sprintf("open loops: %s failed", step.name), logx.Err, err.Error())
			errs = append(errs, fmt.Errorf("%s: %w", step.name, err))
			result = nil
		}
		out = append(out, py.Pair{Key: step.name, Value: result})
	}
	return out, errors.Join(errs...)
}

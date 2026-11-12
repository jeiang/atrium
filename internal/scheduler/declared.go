package scheduler

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/settings"
)

func Declared(defs []Definition, job Job) (Definition, bool) {
	if job.DeclarationKey == nil {
		return Definition{}, false
	}
	i := slices.IndexFunc(defs, func(d Definition) bool { return d.Key == *job.DeclarationKey })
	if i < 0 {
		return Definition{}, false
	}
	return defs[i], true
}

func SpendOf(defs []Definition, job Job) Spend {
	def, _ := Declared(defs, job)
	return def.Spend
}

func declaredSchedule(ctx context.Context, q db.Querier, def Definition) (string, bool, error) {
	found, err := settings.From(ctx).Resolve(ctx, q, def.ScheduleSetting)
	return found.Value, found.Origin != settings.FromDefault, err
}

func syncSchedule(ctx context.Context, q db.Querier, def Definition, job Job, now float64) (bool, error) {
	schedule, explicit, err := declaredSchedule(ctx, q, def)
	if err != nil || !explicit || schedule == job.Schedule {
		return false, err
	}
	_, err = UpdateJob(ctx, q, job.ID, now, JobUpdate{Schedule: &schedule, Actor: Operator})
	var refused Error
	if errors.As(err, &refused) {
		log.Warn(fmt.Sprintf("schedule setting for %s not applied: %s", job.Name, refused), logx.Job, job.ID)
		return false, nil
	}
	return err == nil, err
}

func declaredJob(ctx context.Context, q db.Querier, key string) (*Job, error) {
	var j Job
	err := q.QueryRowContext(ctx, "SELECT "+jobColumns+" FROM jobs WHERE declaration_key = ?", key).Scan(j.fields()...)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &j, nil
}

func Ensure(ctx context.Context, q db.Querier, defs []Definition, tz string, now float64) error {
	rows, err := q.QueryContext(ctx, "SELECT declaration_key FROM jobs WHERE declaration_key IS NOT NULL AND state != ?", StateCancelled)
	if err != nil {
		return err
	}
	existing, err := db.Collect(rows, func(r *sql.Rows) (string, error) {
		var key string
		return key, r.Scan(&key)
	})
	if err != nil {
		return err
	}
	for _, def := range defs {
		job, err := declaredJob(ctx, q, def.Key)
		if err != nil {
			return err
		}
		if job == nil {
			if err := createDeclared(ctx, q, tz, now, def); err != nil {
				return err
			}
			continue
		}
		if job.State == StateCancelled {
			continue
		}
		if _, err := syncSchedule(ctx, q, def, *job, now); err != nil {
			return err
		}
	}
	for _, key := range existing {
		if slices.ContainsFunc(defs, func(d Definition) bool { return d.Key == key }) {
			continue
		}
		if _, err := q.ExecContext(ctx, "UPDATE jobs SET state = ?, next_due = NULL WHERE declaration_key = ? AND state != ?", StateCancelled, key, StateCancelled); err != nil {
			return err
		}
	}
	return nil
}

func createDeclared(ctx context.Context, q db.Querier, tz string, now float64, def Definition) error {
	schedule, _, err := declaredSchedule(ctx, q, def)
	if err != nil {
		return err
	}
	_, err = CreateJob(ctx, q, NewJob{
		Name: def.Name, Prompt: def.Prompt, Schedule: schedule, TZ: tz, Now: now,
		CreatedBy: Operator, Kind: def.Kind, Module: def.Module, Key: def.Key,
	})
	return err
}

package scheduler

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/jeiang/atrium/internal/config"
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
	switch {
	case def.Every > 0:
		return EveryText(def.Every), false, nil
	case def.ScheduleHost != "":
		if value := strings.TrimSpace(config.HostValueFrom(ctx, def.ScheduleHost)); value != "" {
			return value, false, nil
		}
		return def.Schedule, false, nil
	}
	found, err := settings.From(ctx).Resolve(ctx, q, def.ScheduleSetting)
	return found.Value, found.Origin != settings.FromDefault, err
}

func syncSchedule(ctx context.Context, q db.Querier, def Definition, job Job, now float64) (bool, error) {
	schedule, explicit, err := declaredSchedule(ctx, q, def)
	if err != nil || schedule == job.Schedule {
		return false, err
	}
	if def.ScheduleSetting == "" {
		return true, resetSchedule(ctx, q, job, schedule, now)
	}
	if !explicit {
		return false, nil
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

func resetSchedule(ctx context.Context, q db.Querier, job Job, schedule string, now float64) error {
	spec, err := parseJob(schedule, job.TZ)
	if err != nil {
		log.Warn(fmt.Sprintf("declared schedule for %s not applied: %s", job.Name, err), logx.Job, job.ID)
		return nil
	}
	var due *float64
	if job.State == StateActive {
		if next, ok := spec.NextAfter(now); ok {
			due = &next
		}
	}
	_, err = q.ExecContext(ctx, "UPDATE jobs SET schedule = ?, next_due = CASE WHEN state = ? THEN ? ELSE next_due END WHERE id = ?",
		schedule, StateActive, due, job.ID)
	return err
}

func prime(ctx context.Context, q db.Querier, defs []Definition, now float64) error {
	for _, def := range defs {
		if def.Persistent {
			continue
		}
		if def.Every > 0 {
			if _, err := q.ExecContext(ctx, "UPDATE jobs SET next_due = ? WHERE declaration_key = ? AND state = ?",
				now+def.InitialDelay.Seconds(), def.Key, StateActive); err != nil {
				return err
			}
			continue
		}
		job, err := declaredJob(ctx, q, def.Key)
		if err != nil {
			return err
		}
		if job == nil || job.State != StateActive || job.NextDue == nil || !missed(now, *job.NextDue) {
			continue
		}
		spec, err := parseJob(job.Schedule, job.TZ)
		if err != nil {
			continue
		}
		if next, ok := spec.NextAfter(now); ok {
			if _, err := q.ExecContext(ctx, "UPDATE jobs SET next_due = ? WHERE id = ?", next, job.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

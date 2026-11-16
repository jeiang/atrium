package scheduler

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/settings/audit"
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

func runsWhilePaused(defs []Definition, job Job) bool {
	def, _ := Declared(defs, job)
	return def.RunsWhilePaused
}

func SpendOf(defs []Definition, job Job) Spend {
	def, _ := Declared(defs, job)
	return def.Spend
}

var hostWarned sync.Map

const CarryOperation = "scheduler.schedule.carry"

func declaredSchedule(ctx context.Context, q db.Querier, def Definition) string {
	switch {
	case def.Every > 0:
		return EveryText(def.Every)
	case def.ScheduleHost == "":
		return def.Schedule
	}
	value := strings.TrimSpace(config.HostValueFrom(ctx, def.ScheduleHost))
	if value == "" {
		hostWarned.Delete(def.ScheduleHost)
		return def.Schedule
	}
	err := RecurringSchedule(value)
	if err == nil {
		_, _, err = Validate(ctx, q, value, "UTC", 0, true)
	}
	if err == nil {
		hostWarned.Delete(def.ScheduleHost)
		return value
	}
	if last, seen := hostWarned.Swap(def.ScheduleHost, value); !seen || last != value {
		log.Warn(fmt.Sprintf("host schedule %s not applied: %s; using %s", def.ScheduleHost, err, def.Schedule))
	}
	return def.Schedule
}

func syncSchedule(ctx context.Context, tx *sql.Tx, def Definition, job Job, now float64) error {
	if def.CarriedSetting != "" {
		return carry(ctx, tx, def, job, now)
	}
	if schedule := declaredSchedule(ctx, tx, def); schedule != job.Schedule {
		return resetSchedule(ctx, tx, job, schedule, now)
	}
	return nil
}

func carry(ctx context.Context, tx *sql.Tx, def Definition, job Job, now float64) error {
	var value string
	err := tx.QueryRowContext(ctx, "SELECT value FROM settings WHERE key = ?", def.CarriedSetting).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	value = strings.TrimSpace(value)
	if value == "" || value == def.Schedule {
		return nil
	}
	target := "job:" + def.Key
	var done bool
	if err := tx.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM operation_audit WHERE operation = ? AND target = ?)", CarryOperation, target).Scan(&done); err != nil || done {
		return err
	}
	applied, reason := job.Schedule == value, ""
	if job.Schedule == def.Schedule {
		_, err := UpdateJob(ctx, tx, job.ID, now, JobUpdate{Schedule: &value, Actor: Operator, Floor: def.PromptRun})
		var refused Error
		switch {
		case errors.As(err, &refused):
			reason = refused.Error()
			log.Warn(fmt.Sprintf("carried schedule %s for %s not applied: %s", def.CarriedSetting, job.Name, refused), logx.Job, job.ID)
		case err != nil:
			return err
		default:
			applied = true
		}
	}
	details, err := json.Marshal(map[string]any{"key": def.CarriedSetting, "value": value, "applied": applied, "refused": reason})
	if err != nil {
		return err
	}
	_, err = state.New(nil).Audit(ctx, tx, state.Entry{
		Operation: CarryOperation, Actor: "atrium", Source: audit.Migration, Outcome: state.OK, Target: target, DetailsJSON: string(details),
	})
	return err
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

func Ensure(ctx context.Context, tx *sql.Tx, defs []Definition, tz string, now float64) error {
	rows, err := tx.QueryContext(ctx, "SELECT declaration_key FROM jobs WHERE declaration_key IS NOT NULL AND state != ?", StateCancelled)
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
		job, err := declaredJob(ctx, tx, def.Key)
		if err != nil {
			return err
		}
		if job == nil {
			if err := createDeclared(ctx, tx, tz, now, def); err != nil {
				return err
			}
			if job, err = declaredJob(ctx, tx, def.Key); err != nil {
				return err
			}
		}
		if job.State == StateCancelled {
			continue
		}
		if err := syncSchedule(ctx, tx, def, *job, now); err != nil {
			return err
		}
	}
	for _, key := range existing {
		if slices.ContainsFunc(defs, func(d Definition) bool { return d.Key == key }) {
			continue
		}
		if _, err := tx.ExecContext(ctx, "UPDATE jobs SET state = ?, next_due = NULL WHERE declaration_key = ? AND state != ?", StateCancelled, key, StateCancelled); err != nil {
			return err
		}
	}
	return nil
}

func createDeclared(ctx context.Context, q db.Querier, tz string, now float64, def Definition) error {
	_, err := CreateJob(ctx, q, NewJob{
		Name: def.Name, Prompt: def.Prompt, Schedule: declaredSchedule(ctx, q, def), TZ: tz, Now: now,
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
		if def.Backoff.Initial > 0 {
			var retry bool
			if err := q.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM jobs j JOIN job_runs r ON r.id = (SELECT MAX(id) FROM job_runs WHERE job_id = j.id AND COALESCE(owner, '') NOT LIKE 'manual/%') WHERE j.declaration_key = ? AND r.status = ? AND j.next_due IS NOT NULL)", def.Key, StatusError).Scan(&retry); err != nil {
				return err
			}
			if retry {
				continue
			}
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

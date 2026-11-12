package scheduler

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/logx"
	scheddef "github.com/jeiang/atrium/internal/scheduler"
	"github.com/jeiang/atrium/internal/settings"
)

const (
	NightlyName = "nightly"

	WeeklyReviewName    = "weekly-review"
	DailyNudgesName     = "daily-nudges"
	TaskRemindersName   = "task-reminders"
	MemoryWorkName      = "memory-work"
	KnowledgeExportName = "knowledge-export"
)

func Declared(defs []scheddef.Definition, name string) (scheddef.Definition, bool) {
	i := slices.IndexFunc(defs, func(d scheddef.Definition) bool { return d.Name == name })
	if i < 0 {
		return scheddef.Definition{}, false
	}
	return defs[i], true
}

func SpendOf(defs []scheddef.Definition, job Job) scheddef.Spend {
	def, _ := Declared(defs, job.Name)
	return def.Spend
}

func declaredSchedule(ctx context.Context, q db.Querier, def scheddef.Definition) (string, bool, error) {
	found, err := settings.From(ctx).Resolve(ctx, q, def.ScheduleSetting)
	return found.Value, found.Origin != settings.FromDefault, err
}

func syncSchedule(ctx context.Context, q db.Querier, def scheddef.Definition, job Job, now float64) error {
	schedule, explicit, err := declaredSchedule(ctx, q, def)
	if err != nil || !explicit || schedule == job.Schedule {
		return err
	}
	_, err = UpdateJob(ctx, q, job.ID, now, JobUpdate{Schedule: &schedule, Actor: Operator})
	var refused Error
	if errors.As(err, &refused) {
		log.Warn(fmt.Sprintf("schedule setting for %s not applied: %s", job.Name, refused), logx.Job, job.ID)
		return nil
	}
	return err
}

func Ensure(ctx context.Context, q db.Querier, defs []scheddef.Definition, tz string, now float64) error {
	for _, def := range defs {
		if def.Kind == JobNightly {
			if err := ensureSingleton(ctx, q, def, tz, now); err != nil {
				return err
			}
		}
	}
	return ensureBuiltins(ctx, q, defs, tz, now)
}

func ensureBuiltins(ctx context.Context, q db.Querier, defs []scheddef.Definition, tz string, now float64) error {
	rows, err := q.QueryContext(ctx, "SELECT id, name FROM jobs WHERE kind = ?", JobBuiltin)
	if err != nil {
		return err
	}
	existing, err := db.Collect(rows, func(r *sql.Rows) (have, error) {
		var h have
		return h, r.Scan(&h.id, &h.name)
	})
	if err != nil {
		return err
	}
	for _, def := range defs {
		if def.Kind != JobBuiltin {
			continue
		}
		if i := slices.IndexFunc(existing, func(h have) bool { return h.name == def.Name }); i >= 0 {
			job, err := GetJob(ctx, q, existing[i].id)
			if err != nil {
				return err
			}
			if job != nil {
				if err := syncSchedule(ctx, q, def, *job, now); err != nil {
					return err
				}
			}
			continue
		}
		if err := createDeclared(ctx, q, tz, now, def); err != nil {
			return err
		}
	}
	return retireBuiltins(ctx, q, defs, existing)
}

func ensureSingleton(ctx context.Context, q db.Querier, def scheddef.Definition, tz string, now float64) error {
	var id int64
	err := q.QueryRowContext(ctx, "SELECT id FROM jobs WHERE kind = ?", def.Kind).Scan(&id)
	if err == nil {
		job, err := GetJob(ctx, q, id)
		if err != nil || job == nil {
			return err
		}
		return syncSchedule(ctx, q, def, *job, now)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	return createDeclared(ctx, q, tz, now, def)
}

func createDeclared(ctx context.Context, q db.Querier, tz string, now float64, def scheddef.Definition) error {
	schedule, _, err := declaredSchedule(ctx, q, def)
	if err != nil {
		return err
	}
	_, err = CreateJob(ctx, q, NewJob{
		Name: def.Name, Prompt: def.Prompt, Schedule: schedule, TZ: tz, Now: now,
		CreatedBy: Operator, Kind: def.Kind,
	})
	return err
}

type have struct {
	id   int64
	name string
}

func retireBuiltins(ctx context.Context, q db.Querier, defs []scheddef.Definition, existing []have) error {
	for _, h := range existing {
		if _, known := Declared(defs, h.name); known {
			continue
		}
		if _, err := q.ExecContext(ctx, "UPDATE jobs SET state = ?, next_due = NULL WHERE id = ? AND state != ?", StateCancelled, h.id, StateCancelled); err != nil {
			return err
		}
	}
	return nil
}

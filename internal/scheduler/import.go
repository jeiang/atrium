package scheduler

import (
	"context"
	"database/sql"
	"fmt"
	"slices"

	"github.com/jeiang/atrium/internal/db"
)

type importedJob struct {
	id              int64
	name            string
	prompt          string
	schedule        string
	tz              string
	kind            string
	state           string
	nextDue         *float64
	createdBy       string
	createdInThread *int64
	createdAt       float64
	promptBy        *string
}

func ImportJobs(ctx context.Context, src db.Querier, tx *sql.Tx, defs []Definition, now float64) (int64, []string, error) {
	rows, err := src.QueryContext(ctx, "SELECT id, name, prompt, schedule, tz, kind, state, next_due, created_by, created_in_thread, created_at, prompt_by FROM jobs ORDER BY id")
	if err != nil {
		return 0, nil, err
	}
	jobs, err := db.Collect(rows, func(r *sql.Rows) (importedJob, error) {
		var j importedJob
		return j, r.Scan(&j.id, &j.name, &j.prompt, &j.schedule, &j.tz, &j.kind, &j.state, &j.nextDue, &j.createdBy, &j.createdInThread, &j.createdAt, &j.promptBy)
	})
	if err != nil {
		return 0, nil, err
	}
	var notes []string
	var carried int64
	keys := map[string]bool{}
	for _, j := range jobs {
		owner, key := "assistant", (*string)(nil)
		switch j.kind {
		case JobCron:
		case JobNightly, JobBuiltin:
			i := slices.IndexFunc(defs, func(d Definition) bool { return d.Name == j.name && d.Kind == j.kind })
			if i < 0 {
				notes = append(notes, "drop-retired-job "+j.name)
				continue
			}
			if keys[defs[i].Key] {
				notes = append(notes, "drop-duplicate-job "+j.name)
				continue
			}
			keys[defs[i].Key] = true
			owner, key = defs[i].Module, &defs[i].Key
		default:
			return carried, notes, fmt.Errorf("job %d has the unknown kind %q", j.id, j.kind)
		}
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO jobs (id, name, prompt, schedule, tz, kind, state, next_due, created_by, created_in_thread, created_at, prompt_by, module, declaration_key, activated_at) "+
				"VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
			j.id, j.name, j.prompt, j.schedule, j.tz, j.kind, j.state, j.nextDue, j.createdBy, j.createdInThread, j.createdAt, j.promptBy, owner, key, now); err != nil {
			return carried, notes, err
		}
		carried++
	}
	return carried, notes, nil
}

func FinalizeImportedSchedules(ctx context.Context, tx *sql.Tx, defs []Definition, now float64) ([]string, error) {
	var lines []string
	for _, def := range defs {
		job, err := declaredJob(ctx, tx, def.Key)
		if err != nil {
			return nil, err
		}
		if job == nil {
			continue
		}
		before := job.Schedule
		changed, err := syncSchedule(ctx, tx, def, *job, now)
		if err != nil {
			return nil, err
		}
		if changed {
			after, err := GetJob(ctx, tx, job.ID)
			if err != nil || after == nil {
				return nil, err
			}
			lines = append(lines, fmt.Sprintf("jobs: %s takes its schedule setting %q over the carried %q", def.Name, after.Schedule, before))
		}
	}
	return lines, nil
}

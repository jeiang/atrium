package scheduler

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/assistant/budget"
	"github.com/jeiang/atrium/internal/assistant/usage"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/safety"
)

var log = logx.Logger("atrium.agent.scheduler")

const (
	StatusRunning       = "running"
	StatusOK            = "ok"
	StatusError         = "error"
	StatusSkippedBudget = "skipped_budget"
	StatusSkippedPause  = "skipped_pause"
	StatusMissed        = "missed_coalesced"
	workerLost          = "worker lost"
)

type Claim struct {
	Job      Job
	JobRunID int64
	DueAt    float64
	Missed   bool
}

type Skipped struct {
	Job      Job
	JobRunID int64
	Status   string
}

func isNoRows(err error) bool { return errors.Is(err, sql.ErrNoRows) }

type sqlRows = sql.Rows

func ClaimDue(ctx context.Context, d *db.DB, cfg config.Config, loc *time.Location, now float64, owner string) ([]Claim, []Skipped, error) {
	paused := safety.IsPaused(cfg.PausePath())
	overBudget := false
	if _, err := budget.Check(ctx, d.Read, now, loc, true); err != nil {
		if !errors.As(err, new(budget.BudgetExceeded)) {
			return nil, nil, err
		}
		overBudget = true
	}
	var claims []Claim
	var skipped []Skipped
	err := d.WithTx(ctx, func(tx *sql.Tx) error {
		claims, skipped = nil, nil
		r := assistant.Read(ctx, tx)
		lease, grace := r.Int(KeyLeaseSeconds), r.Int(KeyMissedGrace)
		if err := r.Err(); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, "SELECT "+jobColumns+" FROM jobs WHERE enabled = 1 AND cancelled = 0 AND next_due IS NOT NULL "+
			"AND next_due <= ? AND (lease_until IS NULL OR lease_until < ?) ORDER BY next_due, id", now, now)
		if err != nil {
			return err
		}
		due, err := db.Collect(rows, func(r *sql.Rows) (Job, error) {
			var j Job
			return j, r.Scan(j.fields()...)
		})
		if err != nil {
			return err
		}
		for _, job := range due {
			dueAt := *job.NextDue
			spec, err := Parse(job.Schedule, job.TZ)
			if err != nil {
				var bad Error
				if !errors.As(err, &bad) {
					return err
				}
				log.Error(fmt.Sprintf("job %d has an unparsable schedule, skipped: %s", job.ID, err), logx.Job, job.ID)
				continue
			}
			var following *float64
			if spec.Recurring() {
				if next, ok := spec.NextAfter(now); ok {
					following = &next
				}
			}
			if paused || overBudget {
				status := StatusSkippedBudget
				if paused {
					status = StatusSkippedPause
				}
				res, err := tx.ExecContext(ctx, "INSERT INTO job_runs (job_id, due_at, started_at, ended_at, status) VALUES (?, ?, ?, ?, ?)",
					job.ID, dueAt, now, now, status)
				if err != nil {
					return err
				}
				runID, err := res.LastInsertId()
				if err != nil {
					return err
				}
				if _, err := tx.ExecContext(ctx, "UPDATE jobs SET next_due = ?, enabled = ?, last_run_id = ? WHERE id = ?",
					following, following != nil, runID, job.ID); err != nil {
					return err
				}
				skipped = append(skipped, Skipped{Job: job, JobRunID: runID, Status: status})
				continue
			}
			res, err := tx.ExecContext(ctx, "INSERT INTO job_runs (job_id, due_at, started_at, status) VALUES (?, ?, ?, ?)",
				job.ID, dueAt, now, StatusRunning)
			if err != nil {
				return err
			}
			runID, err := res.LastInsertId()
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, "UPDATE jobs SET next_due = ?, enabled = ?, lease_owner = ?, lease_until = ?, last_run_id = ? WHERE id = ?",
				following, following != nil, owner, now+float64(lease), runID, job.ID); err != nil {
				return err
			}
			claims = append(claims, Claim{Job: job, JobRunID: runID, DueAt: dueAt, Missed: now-dueAt > float64(grace)})
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return claims, skipped, nil
}

type Outcome struct {
	Status   string
	ThreadID *int64
	Error    *string
	Tokens   int64
	Cost     float64
}

func FinishRun(ctx context.Context, q db.Querier, c Claim, now float64, o Outcome) error {
	if _, err := q.ExecContext(ctx, "UPDATE job_runs SET status = ?, ended_at = ?, thread_id = COALESCE(?, thread_id), error = ?, tokens = ?, cost = ? WHERE id = ?",
		o.Status, now, o.ThreadID, o.Error, o.Tokens, o.Cost, c.JobRunID); err != nil {
		return err
	}
	_, err := q.ExecContext(ctx, "UPDATE jobs SET lease_owner = NULL, lease_until = NULL WHERE id = ?", c.Job.ID)
	return err
}

func RunTokens(ctx context.Context, q db.Querier, runID int64) (int64, error) {
	var n int64
	err := q.QueryRowContext(ctx, "SELECT COALESCE(SUM(input_tokens + cache_write_5m + cache_write_1h + cache_read + output_tokens), 0) AS n "+
		"FROM usage WHERE run_id = ?", runID).Scan(&n)
	return n, err
}

func ReleaseStale(ctx context.Context, d *db.DB, now float64) error {
	return d.WithTx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT r.id AS run_id, j.id AS job_id FROM job_runs r JOIN jobs j ON j.id = r.job_id "+
			"WHERE r.status = ? AND j.lease_until IS NOT NULL AND j.lease_until < ? AND NOT EXISTS (SELECT 1 FROM runs x WHERE x.id = "+
			"(SELECT MIN(id) FROM runs WHERE thread_id = r.thread_id AND parent_run_id IS NULL) AND x.state IN ('running', 'interrupted'))",
			StatusRunning, now)
		if err != nil {
			return err
		}
		type stale struct{ run, job int64 }
		found, err := db.Collect(rows, func(r *sql.Rows) (stale, error) {
			var s stale
			return s, r.Scan(&s.run, &s.job)
		})
		if err != nil {
			return err
		}
		for _, s := range found {
			if _, err := tx.ExecContext(ctx, "UPDATE job_runs SET status = ?, ended_at = ?, error = ? WHERE id = ?", StatusError, now, workerLost, s.run); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, "UPDATE jobs SET lease_owner = NULL, lease_until = NULL WHERE id = ?", s.job); err != nil {
				return err
			}
		}
		return nil
	})
}

func SettleResumed(ctx context.Context, d *db.DB, now float64) error {
	type settled struct {
		jobRun, job  int64
		thread       int64
		due, started float64
		run          int64
		state        string
		runError     *string
		ended        *float64
	}
	rows, err := d.Read.QueryContext(ctx, "SELECT jr.id, jr.job_id, jr.thread_id, jr.due_at, jr.started_at, r.id, r.state, r.error, r.ended_at "+
		"FROM job_runs jr JOIN runs r ON r.id = (SELECT MIN(id) FROM runs WHERE thread_id = jr.thread_id AND parent_run_id IS NULL) "+
		"WHERE jr.status = ? AND jr.thread_id IS NOT NULL AND r.state IN ('done', 'failed') ORDER BY jr.id", StatusRunning)
	if err != nil {
		return err
	}
	found, err := db.Collect(rows, func(r *sql.Rows) (settled, error) {
		var s settled
		return s, r.Scan(&s.jobRun, &s.job, &s.thread, &s.due, &s.started, &s.run, &s.state, &s.runError, &s.ended)
	})
	if err != nil {
		return err
	}
	r := assistant.Read(ctx, d.Read)
	grace := r.Int(KeyMissedGrace)
	if err := r.Err(); err != nil {
		return err
	}
	for _, s := range found {
		tokens, err := RunTokens(ctx, d.Read, s.run)
		if err != nil {
			return err
		}
		cost, err := usage.RunCost(ctx, d.Read, s.run)
		if err != nil {
			return err
		}
		status, text := StatusOK, (*string)(nil)
		switch {
		case s.state != "done":
			status = StatusError
			if s.runError != nil && *s.runError != "" {
				text = s.runError
			} else {
				text = &s.state
			}
		case s.started-s.due > float64(grace):
			status = StatusMissed
		}
		ended := now
		if s.ended != nil {
			ended = *s.ended
		}
		err = d.WithTx(ctx, func(tx *sql.Tx) error {
			res, err := tx.ExecContext(ctx, "UPDATE job_runs SET status = ?, ended_at = ?, error = ?, tokens = ?, cost = ? WHERE id = ? AND status = ?",
				status, ended, text, tokens, cost, s.jobRun, StatusRunning)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n == 0 {
				return nil
			}
			_, err = tx.ExecContext(ctx, "UPDATE jobs SET lease_owner = NULL, lease_until = NULL WHERE id = ? AND last_run_id = ?", s.job, s.jobRun)
			return err
		})
		if err != nil {
			return err
		}
	}
	return nil
}

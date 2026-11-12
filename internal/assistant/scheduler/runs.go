package scheduler

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/inference"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/safety"
	scheddef "github.com/jeiang/atrium/internal/scheduler"
)

var log = logx.Logger("atrium.agent.scheduler")

type Status string

const (
	StatusRunning       Status = "running"
	StatusOK            Status = "ok"
	StatusError         Status = "error"
	StatusSkippedBudget Status = "skipped_budget"
	StatusSkippedPause  Status = "skipped_pause"
	StatusSkippedModule Status = "skipped_module"
	StatusMissed        Status = "missed_coalesced"
	workerLost                 = "worker lost"
)

func (s Status) Terminal() bool { return s != StatusRunning }

type Claim struct {
	Job      Job
	JobRunID int64
	DueAt    float64
	Missed   bool
}

type Skipped struct {
	Job      Job
	JobRunID int64
	Status   Status
}

func ClaimDue(ctx context.Context, d *db.DB, cfg config.Config, loc *time.Location, now float64, owner string, spend func(Job) scheddef.Spend) ([]Claim, []Skipped, error) {
	paused := safety.IsPaused(cfg.PausePath())
	overBudget := false
	if _, err := inference.Check(ctx, d.Read, now, loc, true); err != nil {
		if !errors.As(err, new(inference.BudgetExceeded)) {
			return nil, nil, err
		}
		overBudget = true
	}
	var claims []Claim
	var skipped []Skipped
	err := d.WithTx(ctx, func(tx *sql.Tx) error {
		claims, skipped = nil, nil
		lease, grace := TuningAgentSchedulerLeaseSeconds, TuningAgentSchedulerMissedGraceSeconds
		rows, err := tx.QueryContext(ctx, "SELECT "+jobColumns+" FROM jobs WHERE state = ? AND next_due IS NOT NULL "+
			"AND next_due <= ? AND (lease_until IS NULL OR lease_until < ?) ORDER BY next_due, id", StateActive, now, now)
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
				log.Error(fmt.Sprintf("job %d has an unparsable schedule, disabled: %s", job.ID, err), logx.Job, job.ID)
				if _, err := tx.ExecContext(ctx, "UPDATE jobs SET state = ?, next_due = NULL WHERE id = ?", StateDisabled, job.ID); err != nil {
					return err
				}
				continue
			}
			var following *float64
			if spec.Recurring() {
				if next, ok := spec.NextAfter(now); ok {
					following = &next
				} else {
					log.Warn(fmt.Sprintf("job %d has no next occurrence; disabling it", job.ID), logx.Job, job.ID)
				}
			}
			if paused || (overBudget && spend(job) == scheddef.AlwaysSpends) {
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
				if _, err := tx.ExecContext(ctx, "UPDATE jobs SET next_due = ?, state = ?, last_run_id = ? WHERE id = ?",
					following, nextState(following), runID, job.ID); err != nil {
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
			if _, err := tx.ExecContext(ctx, "UPDATE jobs SET next_due = ?, state = ?, lease_owner = ?, lease_until = ?, last_run_id = ? WHERE id = ?",
				following, nextState(following), owner, now+float64(lease), runID, job.ID); err != nil {
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
	Status   Status
	ThreadID *int64
	Error    *string
	Tokens   int64
	Cost     float64
}

func nextState(following *float64) State {
	if following == nil {
		return StateDisabled
	}
	return StateActive
}

func settle(ctx context.Context, tx *sql.Tx, jobRunID, jobID int64, now float64, o Outcome) error {
	res, err := tx.ExecContext(ctx, "UPDATE job_runs SET status = ?, ended_at = ?, thread_id = COALESCE(?, thread_id), error = ?, tokens = ?, cost = ? WHERE id = ? AND status = ?",
		o.Status, now, o.ThreadID, o.Error, o.Tokens, o.Cost, jobRunID, StatusRunning)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil
	}
	_, err = tx.ExecContext(ctx, "UPDATE jobs SET lease_owner = NULL, lease_until = NULL WHERE id = ? AND last_run_id = ?", jobID, jobRunID)
	return err
}

func FinishRun(ctx context.Context, d *db.DB, c Claim, now float64, o Outcome) error {
	return d.WithTx(ctx, func(tx *sql.Tx) error {
		return settle(ctx, tx, c.JobRunID, c.Job.ID, now, o)
	})
}

type PendingRun struct {
	Claim     Claim
	Threads   []int64
	StartedAt float64
}

type RecoveredRun struct {
	Claim    Claim
	Outcome  Outcome
	EndedAt  *float64
	Complete bool
}

type Recovery func(context.Context, []PendingRun) ([]RecoveredRun, error)

func pendingRuns(ctx context.Context, q db.Querier) ([]PendingRun, error) {
	rows, err := q.QueryContext(ctx, "SELECT id, job_id, thread_id, due_at, COALESCE(started_at, due_at) FROM job_runs WHERE status = ? ORDER BY id", StatusRunning)
	if err != nil {
		return nil, err
	}
	grace := float64(TuningAgentSchedulerMissedGraceSeconds)
	return db.Collect(rows, func(r *sql.Rows) (PendingRun, error) {
		var p PendingRun
		var thread *int64
		if err := r.Scan(&p.Claim.JobRunID, &p.Claim.Job.ID, &thread, &p.Claim.DueAt, &p.StartedAt); err != nil {
			return p, err
		}
		if thread != nil {
			p.Threads = []int64{*thread}
		}
		p.Claim.Missed = p.StartedAt-p.Claim.DueAt > grace
		return p, nil
	})
}

func Recover(ctx context.Context, d *db.DB, now float64, recover Recovery) error {
	pending, err := pendingRuns(ctx, d.Read)
	if err != nil || len(pending) == 0 {
		return err
	}
	recovered, err := recover(ctx, pending)
	if err != nil {
		return err
	}
	known := make(map[int64]bool, len(recovered))
	for _, r := range recovered {
		known[r.Claim.JobRunID] = true
		if !r.Complete {
			continue
		}
		ended := now
		if r.EndedAt != nil {
			ended = *r.EndedAt
		}
		if err := d.WithTx(ctx, func(tx *sql.Tx) error {
			return settle(ctx, tx, r.Claim.JobRunID, r.Claim.Job.ID, ended, r.Outcome)
		}); err != nil {
			return err
		}
	}
	return d.WithTx(ctx, func(tx *sql.Tx) error {
		for _, p := range pending {
			if known[p.Claim.JobRunID] {
				continue
			}
			var expired bool
			if err := tx.QueryRowContext(ctx, "SELECT lease_until IS NOT NULL AND lease_until < ? FROM jobs WHERE id = ?", now, p.Claim.Job.ID).Scan(&expired); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					continue
				}
				return err
			}
			if !expired {
				continue
			}
			lost := workerLost
			if err := settle(ctx, tx, p.Claim.JobRunID, p.Claim.Job.ID, now, Outcome{Status: StatusError, Error: &lost}); err != nil {
				return err
			}
		}
		return nil
	})
}

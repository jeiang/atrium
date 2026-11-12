package scheduler

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/logx"
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

type Admit func(Job) Status

func DueJobs(ctx context.Context, q db.Querier, now float64) ([]Job, error) {
	rows, err := q.QueryContext(ctx, "SELECT "+jobColumns+" FROM jobs WHERE state = ? AND next_due IS NOT NULL "+
		"AND next_due <= ? AND (lease_until IS NULL OR lease_until < ?) ORDER BY next_due, id", StateActive, now, now)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (Job, error) {
		var j Job
		return j, r.Scan(j.fields()...)
	})
}

func claimJob(ctx context.Context, d *db.DB, jobID int64, now float64, owner string, admit Admit) (*Claim, *Skipped, error) {
	var claim *Claim
	var skipped *Skipped
	err := d.WithTx(ctx, func(tx *sql.Tx) error {
		claim, skipped = nil, nil
		var job Job
		err := tx.QueryRowContext(ctx, "SELECT "+jobColumns+" FROM jobs WHERE id = ? AND state = ? AND next_due IS NOT NULL "+
			"AND next_due <= ? AND (lease_until IS NULL OR lease_until < ?)", jobID, StateActive, now, now).Scan(job.fields()...)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		dueAt := *job.NextDue
		spec, err := Parse(job.Schedule, job.TZ)
		if err != nil {
			var bad Error
			if !errors.As(err, &bad) {
				return err
			}
			log.Error(fmt.Sprintf("job %d has an unparsable schedule, disabled: %s", job.ID, err), logx.Job, job.ID)
			_, err := tx.ExecContext(ctx, "UPDATE jobs SET state = ?, next_due = NULL WHERE id = ?", StateDisabled, job.ID)
			return err
		}
		var following *float64
		if spec.Recurring() {
			if next, ok := spec.NextAfter(now); ok {
				following = &next
			} else {
				log.Warn(fmt.Sprintf("job %d has no next occurrence; disabling it", job.ID), logx.Job, job.ID)
			}
		}
		if status := admit(job); status != "" {
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
			skipped = &Skipped{Job: job, JobRunID: runID, Status: status}
			return nil
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
			following, nextState(following), owner, now+float64(TuningAgentSchedulerLeaseSeconds), runID, job.ID); err != nil {
			return err
		}
		claim = &Claim{Job: job, JobRunID: runID, DueAt: dueAt, Missed: now-dueAt > float64(TuningAgentSchedulerMissedGraceSeconds)}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return claim, skipped, nil
}

func nextState(following *float64) State {
	if following == nil {
		return StateDisabled
	}
	return StateActive
}

func settle(ctx context.Context, tx *sql.Tx, jobRunID, jobID int64, now float64, o Result) error {
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

func FinishRun(ctx context.Context, d *db.DB, c Claim, now float64, o Result) error {
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
	Result   Result
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

func Recover(ctx context.Context, d *db.DB, path string, now float64, recover Recovery) error {
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
			return settle(ctx, tx, r.Claim.JobRunID, r.Claim.Job.ID, ended, r.Result)
		}); err != nil {
			return err
		}
	}
	for _, p := range pending {
		if known[p.Claim.JobRunID] {
			continue
		}
		locks, err := db.TryResourceLocks(path, []string{jobResource(p.Claim.Job.ID)})
		if errors.As(err, new(*db.LockBusy)) {
			continue
		}
		if err != nil {
			return err
		}
		err = d.WithTx(ctx, func(tx *sql.Tx) error {
			var expired bool
			if err := tx.QueryRowContext(ctx, "SELECT lease_until IS NOT NULL AND lease_until < ? FROM jobs WHERE id = ?", now, p.Claim.Job.ID).Scan(&expired); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return nil
				}
				return err
			}
			if !expired {
				return nil
			}
			lost := workerLost
			return settle(ctx, tx, p.Claim.JobRunID, p.Claim.Job.ID, now, Result{Status: StatusError, Error: &lost})
		})
		_ = locks.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func jobResource(id int64) string { return fmt.Sprintf("job/%d", id) }

func claimResources(defs []Definition, job Job) []string {
	resources := []string{jobResource(job.ID)}
	if def, ok := Declared(defs, job.Name); ok && job.Fixed() {
		resources = append(resources, def.Resources...)
	}
	return resources
}

package scheduler

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/jeiang/atrium/internal/db"
)

func RunNow(ctx context.Context, d *db.DB, dbPath, tz string, def Definition, wait time.Duration, fn func(context.Context, Run) (Result, error)) (Result, error) {
	s := New(d, []Definition{def}, Dependencies{DBPath: dbPath, Owner: fmt.Sprintf("manual/%d/%d", os.Getpid(), time.Now().UnixNano())})
	job, err := manualJob(ctx, s, tz, def)
	if err != nil {
		return Result{}, err
	}
	resources := claimResources(s.jobs, job)
	locks, err := db.ResourceLocks(ctx, dbPath, resources, wait)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = locks.Close() }()
	claim, err := claimManual(ctx, d, job.ID, resources, s.now(), s.owner())
	if err != nil {
		return Result{}, err
	}
	var result Result
	var runErr error
	ran := false
	s.execute(db.WithHeld(ctx, locks), *claim, func(ctx context.Context, run Run) (Result, error) {
		result, runErr = fn(ctx, run)
		if runErr == nil && result.Status == "" {
			result.Status = StatusOK
		}
		ran = true
		return result, runErr
	})
	if !ran && runErr == nil {
		runErr = fmt.Errorf("%s crashed", def.Key)
	}
	return result, runErr
}

func manualJob(ctx context.Context, s *Scheduler, tz string, def Definition) (Job, error) {
	var job *Job
	err := s.db.WithTx(ctx, func(tx *sql.Tx) error {
		found, err := declaredJob(ctx, tx, def.Key)
		if err != nil || found != nil {
			job = found
			return err
		}
		if err := createDeclared(ctx, tx, tz, s.now(), def); err != nil {
			return err
		}
		job, err = declaredJob(ctx, tx, def.Key)
		return err
	})
	if err == nil && job == nil {
		err = fmt.Errorf("scheduler: %s has no job row", def.Key)
	}
	if err != nil {
		return Job{}, err
	}
	return *job, nil
}

func claimManual(ctx context.Context, d *db.DB, jobID int64, resources []string, now float64, owner string) (*Claim, error) {
	var claim *Claim
	err := d.WithTx(ctx, func(tx *sql.Tx) error {
		var job Job
		err := tx.QueryRowContext(ctx, "SELECT "+jobColumns+" FROM jobs WHERE id = ? AND (lease_until IS NULL OR lease_until < ?) "+
			"AND NOT EXISTS (SELECT 1 FROM job_runs WHERE job_runs.job_id = jobs.id AND job_runs.status = ?)",
			jobID, now, StatusRunning).Scan(job.fields()...)
		if errors.Is(err, sql.ErrNoRows) {
			return errLeaseHeld
		}
		if err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, "INSERT INTO job_runs (job_id, due_at, started_at, heartbeat_at, owner, status) VALUES (?, ?, ?, ?, ?, ?)",
			job.ID, now, now, now, owner, StatusRunning)
		if err != nil {
			return err
		}
		runID, err := res.LastInsertId()
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE jobs SET lease_owner = ?, lease_until = ?, last_run_id = ? WHERE id = ?",
			owner, leaseUntil(now), runID, job.ID); err != nil {
			return err
		}
		if err := takeLeases(ctx, tx, resources, owner, runID, now); err != nil {
			return err
		}
		claim = &Claim{Job: job, JobRunID: runID, Owner: owner, DueAt: now, StartedAt: now, resources: resources, until: leaseUntil(now), manual: true}
		return nil
	})
	if errors.Is(err, errLeaseHeld) {
		return nil, &db.LockBusy{Resource: jobResource(jobID)}
	}
	return claim, err
}

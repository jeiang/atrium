package scheduler

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/logx"
)

type Claim struct {
	Job       Job
	JobRunID  int64
	Owner     string
	DueAt     float64
	Missed    bool
	resources []string
	until     float64
}

var errLeaseHeld = errors.New("scheduler: a resource lease is still held")

func leaseUntil(now float64) float64 { return now + float64(TuningAgentSchedulerLeaseSeconds) }

func missed(startedAt, dueAt float64) bool {
	return startedAt-dueAt > float64(TuningAgentSchedulerMissedGraceSeconds)
}

func takeLeases(ctx context.Context, tx *sql.Tx, resources []string, owner string, jobRunID int64, now float64) error {
	for _, resource := range resources {
		ok, err := db.Changed(tx.ExecContext(ctx,
			"INSERT INTO job_resource_leases (resource, owner, job_run_id, lease_until) VALUES (?, ?, ?, ?) "+
				"ON CONFLICT (resource) DO UPDATE SET owner = excluded.owner, job_run_id = excluded.job_run_id, lease_until = excluded.lease_until "+
				"WHERE job_resource_leases.lease_until < ?",
			resource, owner, jobRunID, leaseUntil(now), now))
		if err != nil {
			return err
		}
		if !ok {
			return errLeaseHeld
		}
	}
	return nil
}

const claimable = "state = ? AND next_due IS NOT NULL AND next_due <= ? AND (lease_until IS NULL OR lease_until < ?) " +
	"AND NOT EXISTS (SELECT 1 FROM job_runs WHERE job_runs.job_id = jobs.id AND job_runs.status = ?)"

func DueJobs(ctx context.Context, q db.Querier, now float64) ([]Job, error) {
	rows, err := q.QueryContext(ctx, "SELECT "+jobColumns+" FROM jobs WHERE "+claimable+" ORDER BY next_due, id",
		StateActive, now, now, StatusRunning)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (Job, error) {
		var j Job
		return j, r.Scan(j.fields()...)
	})
}

func claimJob(ctx context.Context, d *db.DB, jobID int64, resources []string, now float64, owner string, admit Admit) (*Claim, *Skipped, error) {
	var claim *Claim
	var skipped *Skipped
	err := d.WithTx(ctx, func(tx *sql.Tx) error {
		claim, skipped = nil, nil
		var job Job
		err := tx.QueryRowContext(ctx, "SELECT "+jobColumns+" FROM jobs WHERE id = ? AND "+claimable,
			jobID, StateActive, now, now, StatusRunning).Scan(job.fields()...)
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
		res, err := tx.ExecContext(ctx, "INSERT INTO job_runs (job_id, due_at, started_at, heartbeat_at, owner, status) VALUES (?, ?, ?, ?, ?, ?)",
			job.ID, dueAt, now, now, owner, StatusRunning)
		if err != nil {
			return err
		}
		runID, err := res.LastInsertId()
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE jobs SET next_due = ?, state = ?, lease_owner = ?, lease_until = ?, last_run_id = ? WHERE id = ?",
			following, nextState(following), owner, leaseUntil(now), runID, job.ID); err != nil {
			return err
		}
		if err := takeLeases(ctx, tx, resources, owner, runID, now); err != nil {
			return err
		}
		claim = &Claim{Job: job, JobRunID: runID, Owner: owner, DueAt: dueAt, Missed: missed(now, dueAt), resources: resources, until: leaseUntil(now)}
		return nil
	})
	if errors.Is(err, errLeaseHeld) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	return claim, skipped, nil
}

type expiredRun struct {
	job       Job
	jobRunID  int64
	dueAt     float64
	startedAt float64
}

func expiredRuns(ctx context.Context, q db.Querier, now float64) ([]expiredRun, error) {
	rows, err := q.QueryContext(ctx, "SELECT r.id, r.job_id, r.due_at, COALESCE(r.started_at, r.due_at) FROM job_runs r JOIN jobs j ON j.id = r.job_id "+
		"WHERE r.status = ? AND (j.lease_until IS NULL OR j.lease_until < ?) ORDER BY r.id", StatusRunning, now)
	if err != nil {
		return nil, err
	}
	expired, err := db.Collect(rows, func(r *sql.Rows) (expiredRun, error) {
		var p expiredRun
		return p, r.Scan(&p.jobRunID, &p.job.ID, &p.dueAt, &p.startedAt)
	})
	if err != nil {
		return nil, err
	}
	for i := range expired {
		if err := q.QueryRowContext(ctx, "SELECT "+jobColumns+" FROM jobs WHERE id = ?", expired[i].job.ID).Scan(expired[i].job.fields()...); err != nil {
			return nil, err
		}
	}
	return expired, nil
}

func takeOver(ctx context.Context, d *db.DB, p expiredRun, resources []string, now float64, owner string) (*Claim, error) {
	var claim *Claim
	err := d.WithTx(ctx, func(tx *sql.Tx) error {
		claim = nil
		var job Job
		err := tx.QueryRowContext(ctx, "SELECT "+jobColumns+" FROM jobs WHERE id = ? AND (lease_until IS NULL OR lease_until < ?)",
			p.job.ID, now).Scan(job.fields()...)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		ok, err := db.Changed(tx.ExecContext(ctx, "UPDATE job_runs SET owner = ?, heartbeat_at = ? WHERE id = ? AND job_id = ? AND status = ?",
			owner, now, p.jobRunID, job.ID, StatusRunning))
		if err != nil || !ok {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE jobs SET lease_owner = ?, lease_until = ?, last_run_id = ? WHERE id = ?",
			owner, leaseUntil(now), p.jobRunID, job.ID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM job_resource_leases WHERE job_run_id = ?", p.jobRunID); err != nil {
			return err
		}
		if err := takeLeases(ctx, tx, resources, owner, p.jobRunID, now); err != nil {
			return err
		}
		claim = &Claim{Job: job, JobRunID: p.jobRunID, Owner: owner, DueAt: p.dueAt, Missed: missed(p.startedAt, p.dueAt), resources: resources, until: leaseUntil(now)}
		return nil
	})
	if errors.Is(err, errLeaseHeld) {
		return nil, nil
	}
	return claim, err
}

func (s *Scheduler) Renew(ctx context.Context, c Claim) (float64, error) {
	now := s.now()
	until := leaseUntil(now)
	err := s.db.WithTx(ctx, func(tx *sql.Tx) error {
		ok, err := db.Changed(tx.ExecContext(ctx, "UPDATE jobs SET lease_until = ? WHERE id = ? AND lease_owner = ? AND last_run_id = ?",
			until, c.Job.ID, c.Owner, c.JobRunID))
		if err != nil {
			return err
		}
		if !ok {
			return ErrClaimLost
		}
		ok, err = db.Changed(tx.ExecContext(ctx, "UPDATE job_runs SET heartbeat_at = ? WHERE id = ? AND owner = ? AND status = ?",
			now, c.JobRunID, c.Owner, StatusRunning))
		if err != nil {
			return err
		}
		if !ok {
			return ErrClaimLost
		}
		res, err := tx.ExecContext(ctx, "UPDATE job_resource_leases SET lease_until = ? WHERE owner = ? AND job_run_id = ?",
			until, c.Owner, c.JobRunID)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n != int64(len(c.resources)) {
			return ErrClaimLost
		}
		return nil
	})
	return until, err
}

func (s *Scheduler) Finish(ctx context.Context, c Claim, o Result) error {
	now := s.now()
	return s.db.WithTx(ctx, func(tx *sql.Tx) error {
		ok, err := db.Changed(tx.ExecContext(ctx, "UPDATE job_runs SET status = ?, ended_at = ?, thread_id = COALESCE(?, thread_id), error = ?, tokens = ?, cost = ? "+
			"WHERE id = ? AND owner = ? AND status = ?",
			o.Status, now, o.ThreadID, o.Error, o.Tokens, o.Cost, c.JobRunID, c.Owner, StatusRunning))
		if err != nil {
			return err
		}
		if !ok {
			return ErrClaimLost
		}
		return clearClaim(ctx, tx, c, "lease_owner = NULL, lease_until = NULL")
	})
}

func (s *Scheduler) release(ctx context.Context, c Claim) error {
	return s.db.WithTx(ctx, func(tx *sql.Tx) error {
		return clearClaim(ctx, tx, c, "lease_until = 0")
	})
}

func clearClaim(ctx context.Context, tx *sql.Tx, c Claim, set string) error {
	ok, err := db.Changed(tx.ExecContext(ctx, "UPDATE jobs SET "+set+" WHERE id = ? AND lease_owner = ? AND last_run_id = ?",
		c.Job.ID, c.Owner, c.JobRunID))
	if err != nil {
		return err
	}
	if !ok {
		return ErrClaimLost
	}
	_, err = tx.ExecContext(ctx, "DELETE FROM job_resource_leases WHERE owner = ? AND job_run_id = ?", c.Owner, c.JobRunID)
	return err
}

func ClaimedThreads(ctx context.Context, q db.Querier) (map[int64]bool, error) {
	rows, err := q.QueryContext(ctx, "SELECT t.thread_id FROM job_run_threads t JOIN job_runs r ON r.id = t.job_run_id WHERE r.status = ?", StatusRunning)
	if err != nil {
		return nil, err
	}
	ids, err := db.Collect(rows, func(r *sql.Rows) (int64, error) {
		var id int64
		return id, r.Scan(&id)
	})
	if err != nil {
		return nil, err
	}
	out := make(map[int64]bool, len(ids))
	for _, id := range ids {
		out[id] = true
	}
	return out, nil
}

func jobResource(id int64) string { return fmt.Sprintf("job/%d", id) }

func claimResources(defs []Definition, job Job) []string {
	resources := []string{jobResource(job.ID)}
	if def, ok := Declared(defs, job); ok {
		resources = append(resources, def.Resources...)
	}
	slices.Sort(resources)
	return slices.Compact(resources)
}

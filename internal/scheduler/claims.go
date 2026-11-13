package scheduler

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/runtime"
)

type Claim struct {
	Job       Job
	JobRunID  int64
	Owner     string
	DueAt     float64
	Missed    bool
	StartedAt float64
	Previous  string
	resources []string
	until     float64
	manual    bool
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
	"AND NOT EXISTS (SELECT 1 FROM job_runs WHERE job_runs.job_id = jobs.id AND (job_runs.status = ? OR (job_runs.status = 'skipped_module' AND job_runs.ended_at IS NULL)))"

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
		spec, err := parseJob(job.Schedule, job.TZ)
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
			if status == StatusSkippedModule {
				var runID int64
				err := tx.QueryRowContext(ctx, "SELECT id FROM job_runs WHERE job_id = ? AND status = ? AND ended_at IS NOT NULL AND id = (SELECT MAX(id) FROM job_runs WHERE job_id = ?) AND NOT EXISTS (SELECT 1 FROM job_run_threads WHERE job_run_id = job_runs.id)", job.ID, status, job.ID).Scan(&runID)
				if err == nil {
					if _, err := tx.ExecContext(ctx, "UPDATE job_runs SET due_at = ?, ended_at = ? WHERE id = ?", dueAt, now, runID); err != nil {
						return err
					}
					if _, err := tx.ExecContext(ctx, "UPDATE jobs SET next_due = ?, state = ?, last_run_id = ? WHERE id = ?", following, nextState(following), runID, job.ID); err != nil {
						return err
					}
					skipped = &Skipped{Job: job, JobRunID: runID, Status: status}
					return nil
				}
				if !errors.Is(err, sql.ErrNoRows) {
					return err
				}
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
		claim = &Claim{Job: job, JobRunID: runID, Owner: owner, DueAt: dueAt, Missed: missed(now, dueAt), StartedAt: now, resources: resources, until: leaseUntil(now)}
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
		"WHERE (r.status = ? OR (r.status = 'skipped_module' AND r.ended_at IS NULL)) AND (j.lease_until IS NULL OR j.lease_until < ?) ORDER BY r.id", StatusRunning, now)
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
		var previous string
		if err := tx.QueryRowContext(ctx, "SELECT COALESCE(owner, '') FROM job_runs WHERE id = ? AND job_id = ? AND (status = ? OR (status = 'skipped_module' AND ended_at IS NULL))",
			p.jobRunID, job.ID, StatusRunning).Scan(&previous); errors.Is(err, sql.ErrNoRows) {
			return nil
		} else if err != nil {
			return err
		}
		manual := strings.HasPrefix(previous, "manual/")
		if manual {
			owner = "manual/" + owner
		}
		ok, err := db.Changed(tx.ExecContext(ctx, "UPDATE job_runs SET owner = ?, heartbeat_at = ?, status = ? WHERE id = ? AND job_id = ? AND (status = ? OR (status = 'skipped_module' AND ended_at IS NULL))",
			owner, now, StatusRunning, p.jobRunID, job.ID, StatusRunning))
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
		claim = &Claim{Job: job, JobRunID: p.jobRunID, Owner: owner, DueAt: p.dueAt, Missed: missed(p.startedAt, p.dueAt), StartedAt: p.startedAt, Previous: previous,
			resources: resources, until: leaseUntil(now), manual: manual}
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
		ok, err := db.Changed(tx.ExecContext(ctx, "UPDATE jobs SET lease_until = ? WHERE id = ? AND lease_owner = ? AND last_run_id = ? AND lease_until >= ?",
			until, c.Job.ID, c.Owner, c.JobRunID, now))
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
		if err := clearClaim(ctx, tx, c, "lease_owner = NULL, lease_until = NULL"); err != nil {
			return err
		}
		def, declared := Declared(s.jobs, c.Job)
		if o.Status != StatusError || c.manual || !declared || def.Backoff == (runtime.Backoff{}) {
			return nil
		}
		return backOff(ctx, tx, c.Job.ID, def.Backoff, now)
	})
}

func backOff(ctx context.Context, tx *sql.Tx, jobID int64, b runtime.Backoff, now float64) error {
	var failures int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM job_runs WHERE job_id = ? AND COALESCE(owner, '') NOT LIKE 'manual/%' AND status = ? AND id > "+
		"COALESCE((SELECT MAX(id) FROM job_runs WHERE job_id = ? AND COALESCE(owner, '') NOT LIKE 'manual/%' AND status NOT IN (?, ?)), 0)",
		jobID, StatusError, jobID, StatusError, StatusRunning).Scan(&failures); err != nil {
		return err
	}
	delay := b.Initial
	for i := 1; i < failures && delay < b.Maximum; i++ {
		delay *= 2
	}
	retry := now + min(delay, b.Maximum).Seconds()
	_, err := tx.ExecContext(ctx, "UPDATE jobs SET next_due = ? WHERE id = ? AND state = ? AND (next_due IS NULL OR next_due > ?)",
		retry, jobID, StateActive, retry)
	return err
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
	rows, err := q.QueryContext(ctx, "SELECT t.thread_id FROM job_run_threads t JOIN job_runs r ON r.id = t.job_run_id WHERE r.status = ? OR (r.status = 'skipped_module' AND r.ended_at IS NULL)", StatusRunning)
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

func (c Claim) RemoveFrom(ctx context.Context, q db.Querier) error {
	if c.JobRunID == 0 {
		return nil
	}
	if _, err := q.ExecContext(ctx, "DELETE FROM job_resource_leases WHERE job_run_id = ?", c.JobRunID); err != nil {
		return err
	}
	if _, err := q.ExecContext(ctx, "UPDATE jobs SET lease_owner = NULL, lease_until = NULL, last_run_id = (SELECT MAX(id) FROM job_runs WHERE job_id = jobs.id AND id <> ?) WHERE id = ? AND last_run_id = ?",
		c.JobRunID, c.Job.ID, c.JobRunID); err != nil {
		return err
	}
	_, err := q.ExecContext(ctx, "DELETE FROM job_runs WHERE id = ?", c.JobRunID)
	return err
}

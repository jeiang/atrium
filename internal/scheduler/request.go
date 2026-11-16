package scheduler

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/logx"
)

const (
	requestPoll    = 250 * time.Millisecond
	requestExpires = time.Minute
)

var errRequestGone = errors.New("scheduler: the requested run is gone")

type Requested struct {
	ID     int64
	Status Status
	Ended  bool
	Error  string
	Owner  string
}

func Request(ctx context.Context, d *db.DB, key string, now float64) (int64, error) {
	var jobID int64
	err := d.Read.QueryRowContext(ctx, "SELECT id FROM jobs WHERE declaration_key = ?", key).Scan(&jobID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("scheduler: no declared job %s", key)
	}
	if err != nil {
		return 0, err
	}
	return RequestJob(ctx, d, jobID, now)
}

func RequestJob(ctx context.Context, d *db.DB, jobID int64, now float64) (int64, error) {
	var id int64
	err := d.WithTx(ctx, func(tx *sql.Tx) error {
		var state State
		err := tx.QueryRowContext(ctx, "SELECT state FROM jobs WHERE id = ?", jobID).Scan(&state)
		if errors.Is(err, sql.ErrNoRows) || state == StateCancelled {
			return errNoActive(jobID)
		}
		if err != nil {
			return err
		}
		var running int64
		err = tx.QueryRowContext(ctx, "SELECT id FROM job_runs WHERE job_id = ? AND status = ? ORDER BY id DESC LIMIT 1", jobID, StatusRunning).Scan(&running)
		if err == nil {
			return &Running{JobID: jobID, RunID: running}
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		res, err := tx.ExecContext(ctx, "INSERT INTO job_runs (job_id, due_at, owner, status) VALUES (?, ?, ?, ?)",
			jobID, now, fmt.Sprintf("request/%d", os.Getpid()), StatusRequested)
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		return err
	})
	return id, err
}

func RequestState(ctx context.Context, q db.Querier, id int64) (Requested, error) {
	var r Requested
	var ended sql.NullFloat64
	var text, owner sql.NullString
	err := q.QueryRowContext(ctx, "SELECT status, ended_at, error, owner FROM job_runs WHERE id = ?", id).Scan(&r.Status, &ended, &text, &owner)
	if errors.Is(err, sql.ErrNoRows) {
		return Requested{}, errRequestGone
	}
	r.ID, r.Ended, r.Error, r.Owner = id, ended.Valid, text.String, owner.String
	return r, err
}

func Withdraw(ctx context.Context, d *db.DB, id int64) (bool, error) {
	return db.Changed(d.Write.ExecContext(ctx, "DELETE FROM job_runs WHERE id = ? AND status IN (?, ?)", id, StatusRequested, StatusRefused))
}

func (s *Scheduler) serveRequests(ctx context.Context) {
	ticker := time.NewTicker(requestPoll)
	defer ticker.Stop()
	for {
		if err := s.takeRequests(ctx); err != nil && ctx.Err() == nil {
			log.Error("taking requested runs failed", logx.Err, err)
		}
		select {
		case <-ctx.Done():
			return
		case <-s.stop:
			return
		case <-ticker.C:
		}
	}
}

func (s *Scheduler) takeRequests(ctx context.Context) error {
	rows, err := s.db.Read.QueryContext(ctx, "SELECT id, due_at, job_id FROM job_runs WHERE status = ? ORDER BY id", StatusRequested)
	if err != nil {
		return err
	}
	type request struct {
		id  int64
		at  float64
		job Job
	}
	pending, err := db.Collect(rows, func(r *sql.Rows) (request, error) {
		var p request
		return p, r.Scan(&p.id, &p.at, &p.job.ID)
	})
	if err != nil {
		return err
	}
	for _, p := range pending {
		if s.stopping() {
			return nil
		}
		if err := s.db.Read.QueryRowContext(ctx, "SELECT "+jobColumns+" FROM jobs WHERE id = ?", p.job.ID).Scan(p.job.fields()...); err != nil {
			return err
		}
		if err := s.runRequest(ctx, p.id, p.at, p.job); err != nil {
			return err
		}
	}
	return nil
}

func (s *Scheduler) runRequest(ctx context.Context, id int64, at float64, job Job) error {
	now := s.now()
	if _, declared := Declared(s.jobs, job); !declared && job.Fixed() {
		return s.refuse(ctx, id, "not a declared job")
	}
	if job.State == StateCancelled {
		return s.refuse(ctx, id, "the schedule was deleted")
	}
	if now-at > requestExpires.Seconds() {
		return s.refuse(ctx, id, "the request expired before serve took it")
	}
	if s.deps.Gate != nil {
		gate, err := s.deps.Gate(ctx, now)
		if err != nil {
			return err
		}
		if status := s.admit(gate)(job); status != "" {
			return s.skipRequest(ctx, id, job.ID, status, now)
		}
	}
	if busy := s.enter(job.ID); busy != nil {
		return s.refuseRunning(ctx, id, busy)
	}
	resources := claimResources(s.jobs, job)
	locks, err := db.TryResourceLocks(s.deps.DBPath, resources)
	if err == nil {
		var claim *Claim
		claim, err = claimManual(ctx, s.db, job.ID, id, resources, now, "manual/"+s.owner())
		if err == nil {
			s.hold(job.ID, claim.JobRunID)
			s.start(ctx, *claim, locks, s.work(claim.Job))
			return nil
		}
		_ = locks.Close()
	}
	s.leave(job.ID)
	switch {
	case errors.Is(err, errRequestGone):
		return nil
	case errors.As(err, new(*db.LockBusy)):
		running := &Running{JobID: job.ID}
		if qerr := s.db.Read.QueryRowContext(ctx, "SELECT id FROM job_runs WHERE job_id = ? AND status = ? ORDER BY id DESC LIMIT 1", job.ID, StatusRunning).Scan(&running.RunID); qerr != nil && !errors.Is(qerr, sql.ErrNoRows) {
			return qerr
		}
		return s.refuseRunning(ctx, id, running)
	}
	return err
}

func (s *Scheduler) refuseRunning(ctx context.Context, id int64, running *Running) error {
	if running.RunID == 0 {
		return s.refuse(ctx, id, "already running")
	}
	return s.refuse(ctx, id, fmt.Sprintf("already running (run %d)", running.RunID))
}

func (s *Scheduler) refuse(ctx context.Context, id int64, reason string) error {
	_, err := s.db.Write.ExecContext(ctx, "UPDATE job_runs SET status = ?, ended_at = ?, error = ? WHERE id = ? AND status = ?",
		StatusRefused, s.now(), reason, id, StatusRequested)
	return err
}

func (s *Scheduler) skipRequest(ctx context.Context, id, jobID int64, status Status, now float64) error {
	return s.db.WithTx(ctx, func(tx *sql.Tx) error {
		ok, err := db.Changed(tx.ExecContext(ctx, "UPDATE job_runs SET status = ?, started_at = ?, ended_at = ?, owner = NULL WHERE id = ? AND status = ?",
			status, now, now, id, StatusRequested))
		if err != nil || !ok {
			return err
		}
		_, err = tx.ExecContext(ctx, "UPDATE jobs SET last_run_id = ? WHERE id = ?", id, jobID)
		return err
	})
}

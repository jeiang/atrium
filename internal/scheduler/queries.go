package scheduler

import (
	"context"
	"database/sql"
	"errors"

	"github.com/jeiang/atrium/internal/db"
)

type CompletedRun struct {
	ID        int64
	JobID     int64
	ThreadID  *int64
	Status    Status
	StartedAt float64
}

type Restore struct {
	ID       int64
	Name     string
	Prompt   string
	Schedule string
	State    string
	NextDue  *float64
}

func AttachThread(ctx context.Context, q db.Querier, c Claim, threadID int64) error {
	_, err := q.ExecContext(ctx, "UPDATE job_runs SET thread_id = ? WHERE id = ? AND job_id = ? AND status = ?",
		threadID, c.JobRunID, c.Job.ID, StatusRunning)
	return err
}

func PreviousStatus(ctx context.Context, q db.Querier, jobID, jobRunID int64) (*Status, error) {
	var status Status
	err := q.QueryRowContext(ctx, "SELECT status FROM job_runs WHERE job_id = ? AND id < ? ORDER BY id DESC LIMIT 1",
		jobID, jobRunID).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &status, nil
}

func LatestCompleted(ctx context.Context, q db.Querier, name string, since float64) (*CompletedRun, error) {
	var run CompletedRun
	err := q.QueryRowContext(ctx,
		"SELECT jr.id, jr.job_id, jr.thread_id, jr.status, COALESCE(jr.started_at, jr.due_at) FROM job_runs jr JOIN jobs j ON j.id = jr.job_id "+
			"WHERE j.name = ? AND jr.ended_at IS NOT NULL AND jr.ended_at >= ? ORDER BY jr.id DESC LIMIT 1", name, since).
		Scan(&run.ID, &run.JobID, &run.ThreadID, &run.Status, &run.StartedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &run, nil
}

func DeleteAgentJob(ctx context.Context, q db.Querier, id int64) (bool, error) {
	return db.Changed(q.ExecContext(ctx, "DELETE FROM jobs WHERE id = ? AND created_by = ? AND state = ?", id, Agent, StateActive))
}

func RestoreJob(ctx context.Context, q db.Querier, r Restore) (bool, error) {
	return db.Changed(q.ExecContext(ctx, "UPDATE jobs SET name = ?, prompt = ?, schedule = ?, state = ?, next_due = ? WHERE id = ?",
		r.Name, r.Prompt, r.Schedule, r.State, r.NextDue, r.ID))
}

func JobName(ctx context.Context, q db.Querier, id int64) (string, error) {
	var name string
	err := q.QueryRowContext(ctx, "SELECT name FROM jobs WHERE id = ?", id).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return name, err
}

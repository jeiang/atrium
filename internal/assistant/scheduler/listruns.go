package scheduler

import (
	"context"
	"database/sql"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/db/readpaths"
)

const defaultRunLimit = 30

type ListedRun struct {
	ID        int64    `json:"id"`
	JobID     int64    `json:"job_id"`
	ThreadID  *int64   `json:"thread_id"`
	DueAt     float64  `json:"due_at"`
	StartedAt *float64 `json:"started_at"`
	EndedAt   *float64 `json:"ended_at"`
	Status    string   `json:"status"`
	Error     *string  `json:"error"`
	Tokens    int64    `json:"tokens"`
	Cost      float64  `json:"cost"`
	JobName   string   `json:"job_name"`
	JobKind   string   `json:"job_kind"`
	RunID     *int64   `json:"run_id"`
}

func ListRuns(ctx context.Context, q db.Querier, jobID int64, limit int) ([]ListedRun, error) {
	where, args := "", []any{limit}
	if jobID != 0 {
		where, args = "WHERE r.job_id = ? ", []any{jobID, limit}
	}
	rows, err := q.QueryContext(ctx,
		"SELECT r.id, r.job_id, r.thread_id, r.due_at, r.started_at, r.ended_at, r.status, r.error, r.tokens, r.cost, "+
			"j.name, j.kind, (SELECT id FROM runs x WHERE x.thread_id = r.thread_id ORDER BY id DESC LIMIT 1) "+
			"FROM job_runs r JOIN jobs j ON j.id = r.job_id "+where+"ORDER BY r.id DESC LIMIT ?", args...)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (ListedRun, error) {
		var run ListedRun
		return run, r.Scan(&run.ID, &run.JobID, &run.ThreadID, &run.DueAt, &run.StartedAt, &run.EndedAt, &run.Status, &run.Error,
			&run.Tokens, &run.Cost, &run.JobName, &run.JobKind, &run.RunID)
	})
}

func init() {
	readpaths.Register(readpaths.Path{
		Name:   "scheduler.list_runs",
		Golden: "scheduler.list_runs",
		Run: func(ctx context.Context, d *db.DB) (any, error) {
			return ListRuns(ctx, d.Read, 0, defaultRunLimit)
		},
	})
}

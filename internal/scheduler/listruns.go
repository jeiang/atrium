package scheduler

import (
	"context"
	"database/sql"
	"strings"

	"github.com/jeiang/atrium/internal/db"
)

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
}

func JobExists(ctx context.Context, q db.Querier, id int64) (bool, error) {
	var exists bool
	err := q.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM jobs WHERE id = ?)", id).Scan(&exists)
	return exists, err
}

func ListRuns(ctx context.Context, q db.Querier, jobID, before int64, limit int) ([]ListedRun, error) {
	var conds []string
	var args []any
	if jobID != 0 {
		conds, args = append(conds, "r.job_id = ?"), append(args, jobID)
	}
	if before > 0 {
		conds, args = append(conds, "r.id < ?"), append(args, before)
	}
	where := ""
	if len(conds) > 0 {
		where = "WHERE " + strings.Join(conds, " AND ") + " "
	}
	args = append(args, limit)
	rows, err := q.QueryContext(ctx,
		"SELECT r.id, r.job_id, r.thread_id, r.due_at, r.started_at, r.ended_at, r.status, r.error, r.tokens, r.cost, j.name, j.kind "+
			"FROM job_runs r JOIN jobs j ON j.id = r.job_id "+where+"ORDER BY r.id DESC LIMIT ?", args...)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (ListedRun, error) {
		var run ListedRun
		return run, r.Scan(&run.ID, &run.JobID, &run.ThreadID, &run.DueAt, &run.StartedAt, &run.EndedAt, &run.Status, &run.Error,
			&run.Tokens, &run.Cost, &run.JobName, &run.JobKind)
	})
}

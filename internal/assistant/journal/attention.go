package journal

import (
	"context"
	"database/sql"

	"github.com/jeiang/atrium/internal/db"
)

type ParkedQuestion struct {
	ThreadID  int64
	Title     string
	ArgsJSON  string
	StartedAt float64
}

func ParkedQuestions(ctx context.Context, q db.Querier) ([]ParkedQuestion, error) {
	rows, err := q.QueryContext(ctx,
		"SELECT r.thread_id, COALESCE(t.title, ''), c.args_json, c.started_at FROM tool_calls c "+
			"JOIN runs r ON r.id = c.run_id LEFT JOIN threads t ON t.id = r.thread_id "+
			"WHERE r.state = 'parked' AND c.name = 'ask_operator' AND c.state = 'started' ORDER BY c.id DESC")
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (ParkedQuestion, error) {
		var q ParkedQuestion
		err := r.Scan(&q.ThreadID, &q.Title, &q.ArgsJSON, &q.StartedAt)
		return q, err
	})
}

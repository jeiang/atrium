package memory

import (
	"context"
	"database/sql"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/textsearch"
	"github.com/jeiang/atrium/internal/tool"
)

type SearchFilter struct {
	Query   string
	Project int64
	Limit   int
}

type Hit struct {
	ThreadID  int64
	Title     *string
	Kind      string
	ProjectID int64
	FromSeq   int64
	ToSeq     int64
	State     string
	Text      string
	CreatedAt float64
	Marks     tool.Marks
}

const searchSQL = "SELECT n.thread_id, t.title, t.kind, COALESCE(t.project_id, 0), n.src_from_seq, n.src_to_seq, n.state, n.text, n.created_at, t.marks " +
	"FROM memory_fts f JOIN memory_nodes n ON n.thread_id = f.thread_id AND n.level = f.level AND n.idx = f.idx " +
	"JOIN threads t ON t.id = n.thread_id LEFT JOIN projects p ON p.id = t.project_id " +
	"WHERE memory_fts MATCH ? AND n.state IN (?, ?, ?) AND t.deleted_at IS NULL " +
	"AND (t.project_id IS NULL OR (p.id IS NOT NULL AND (p.private = 0 OR t.project_id = (SELECT project_id FROM threads WHERE id = ?))))"

func Search(ctx context.Context, q db.Querier, reader int64, f SearchFilter) ([]Hit, error) {
	match, err := textsearch.FTSQuery(f.Query)
	if err != nil {
		return nil, err
	}
	query := searchSQL
	args := []any{match, Summary, Fallback, Direct, reader}
	if f.Project != 0 {
		query += " AND t.project_id = ?"
		args = append(args, f.Project)
	}
	query += " ORDER BY rank LIMIT ?"
	args = append(args, f.Limit)
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (Hit, error) {
		var h Hit
		var marks int64
		err := r.Scan(&h.ThreadID, &h.Title, &h.Kind, &h.ProjectID, &h.FromSeq, &h.ToSeq, &h.State, &h.Text, &h.CreatedAt, &marks)
		h.Marks = tool.Marks(marks)
		return h, err
	})
}

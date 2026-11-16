package threads

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/textsearch"
)

const FindRows = 1000

type Found struct {
	ID          int64
	Title       *string
	ProjectName *string
	UpdatedAt   float64
	Snippet     string
}

func Find(ctx context.Context, q db.Querier, match string, limit, offset int) ([]Found, error) {
	rows, err := q.QueryContext(ctx, fmt.Sprintf("SELECT t.id, t.title, (SELECT name FROM projects p WHERE p.id = t.project_id), t.updated_at, "+
		"snippet(thread_fts, 1, '%s', '%s', '…', %d) "+
		"FROM thread_fts JOIN threads t ON t.id = thread_fts.thread_id "+
		"WHERE thread_fts MATCH ? AND t.kind = ? AND t.deleted_at IS NULL ORDER BY bm25(thread_fts) LIMIT ?",
		textsearch.MarkStart, textsearch.MarkEnd, textsearch.SnippetTokens), match, Chat, FindRows)
	if err != nil {
		return nil, err
	}
	all, err := db.Collect(rows, func(r *sql.Rows) (Found, error) {
		var f Found
		return f, r.Scan(&f.ID, &f.Title, &f.ProjectName, &f.UpdatedAt, &f.Snippet)
	})
	if err != nil {
		return nil, err
	}
	seen := map[int64]bool{}
	var out []Found
	for _, f := range all {
		if seen[f.ID] {
			continue
		}
		seen[f.ID] = true
		if len(seen) > offset {
			out = append(out, f)
		}
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

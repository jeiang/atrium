package notion

import (
	"context"
	"database/sql"

	"github.com/jeiang/atrium/internal/db"
)

type ImportPage struct {
	ID       string
	Parent   string
	Title    string
	Markdown string
	Private  int64
	Aliases  string
}

func ImportPages(ctx context.Context, q db.Querier) ([]ImportPage, error) {
	rows, err := q.QueryContext(ctx,
		"SELECT p.id, p.parent_id, p.title, p.markdown, p.private, COALESCE(w.aliases, '[]') "+
			"FROM notion_pages p LEFT JOIN wiki_pages w ON w.page_id = p.id WHERE p.in_trash = 0 AND p.markdown != ''")
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (ImportPage, error) {
		var p ImportPage
		err := r.Scan(&p.ID, &p.Parent, &p.Title, &p.Markdown, &p.Private, &p.Aliases)
		return p, err
	})
}

func WikiPageIDs(ctx context.Context, q db.Querier) ([]string, error) {
	rows, err := q.QueryContext(ctx, "SELECT page_id FROM wiki_pages")
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (string, error) {
		var id string
		err := r.Scan(&id)
		return id, err
	})
}

func Titles(ctx context.Context, q db.Querier) (map[string]string, error) {
	rows, err := q.QueryContext(ctx, "SELECT id, title FROM notion_pages WHERE title != ''")
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for rows.Next() {
		var id, title string
		if err := rows.Scan(&id, &title); err != nil {
			_ = rows.Close()
			return nil, err
		}
		out[id] = title
	}
	return out, rows.Err()
}

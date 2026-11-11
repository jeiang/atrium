package embedding

import (
	"context"
	"database/sql"

	"github.com/jeiang/atrium/internal/db"
)

type Page struct {
	RID          int64
	Title        string
	Path         string
	Markdown     string
	Private      int64
	EmbeddedHash string
}

func scan(r *sql.Rows) (Page, error) {
	var p Page
	var hash sql.NullString
	err := r.Scan(&p.RID, &p.Title, &p.Path, &p.Markdown, &p.Private, &hash)
	p.EmbeddedHash = hash.String
	return p, err
}

func Pending(ctx context.Context, q db.Querier, where string, limit int, after int64) ([]Page, error) {
	query := "SELECT rid, title, path, markdown, private, embedded_hash FROM notion_pages WHERE in_trash = 0"
	if where != "" {
		query += " AND " + where
	}
	rows, err := q.QueryContext(ctx, query+" AND rid > ? ORDER BY rid LIMIT ?", after, limit)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, scan)
}

func Load(ctx context.Context, q db.Querier, rid int64) (Page, bool, error) {
	rows, err := q.QueryContext(ctx, "SELECT rid, title, path, markdown, private, embedded_hash FROM notion_pages WHERE in_trash = 0 AND rid = ?", rid)
	if err != nil {
		return Page{}, false, err
	}
	found, err := db.Collect(rows, scan)
	if err != nil || len(found) == 0 {
		return Page{}, false, err
	}
	return found[0], true, nil
}

func Mark(ctx context.Context, q db.Querier, rid int64, hash string) error {
	_, err := q.ExecContext(ctx, "UPDATE notion_pages SET embedded_hash = ? WHERE rid = ?", hash, rid)
	return err
}

func ClearIn(ctx context.Context, q db.Querier, table string) error {
	_, err := q.ExecContext(ctx, "UPDATE notion_pages SET embedded_hash = NULL WHERE rid IN (SELECT rowid FROM "+table+")")
	return err
}

func Counts(ctx context.Context, q db.Querier, table string) (done, total int, err error) {
	if err := q.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM notion_pages WHERE in_trash = 0 AND rid IN (SELECT rowid FROM "+table+")").Scan(&done); err != nil {
		return 0, 0, err
	}
	if err := q.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM notion_pages WHERE in_trash = 0").Scan(&total); err != nil {
		return 0, 0, err
	}
	return done, total, nil
}

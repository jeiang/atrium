package threads

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/jeiang/atrium/internal/db"
)

const TodayRole = "today"

func TodayTitle(day time.Time) string {
	return "Today · " + day.Format("2 Jan 2006")
}

func Today(ctx context.Context, d *db.DB, now float64, day time.Time) (int64, error) {
	var id int64
	err := d.WithTx(ctx, func(tx *sql.Tx) error {
		var err error
		id, _, err = TodayTx(ctx, tx, now, day)
		return err
	})
	return id, err
}

func TodayTx(ctx context.Context, q db.Querier, now float64, day time.Time) (int64, bool, error) {
	title := TodayTitle(day)
	var id int64
	err := q.QueryRowContext(ctx,
		"SELECT id FROM threads WHERE kind = ? AND role = ? AND title = ? AND deleted_at IS NULL ORDER BY id LIMIT 1",
		Chat, TodayRole, title).Scan(&id)
	if !errors.Is(err, sql.ErrNoRows) {
		return id, false, err
	}
	role := TodayRole
	id, err = Create(ctx, q, now, Spec{Kind: Chat, Title: &title, Role: &role})
	return id, err == nil, err
}

func HasChat(ctx context.Context, q db.Querier) (bool, error) {
	var found bool
	err := q.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM threads WHERE kind = ? AND deleted_at IS NULL)", Chat).Scan(&found)
	return found, err
}

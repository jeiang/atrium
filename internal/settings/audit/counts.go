package audit

import (
	"context"
	"database/sql"
	"errors"

	"github.com/jeiang/atrium/internal/db"
)

func CountForKeySource(ctx context.Context, q db.Querier, key, source string) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM settings_audit WHERE key = ? AND source = ?", key, source).Scan(&n)
	return n, err
}

func NewValueForKeySource(ctx context.Context, q db.Querier, key, source string) (string, error) {
	var value string
	err := q.QueryRowContext(ctx, "SELECT new_value FROM settings_audit WHERE key = ? AND source = ? ORDER BY id LIMIT 1", key, source).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return value, err
}

func CountForKey(ctx context.Context, q db.Querier, key string) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM settings_audit WHERE key = ?", key).Scan(&n)
	return n, err
}

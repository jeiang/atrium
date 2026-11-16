package accounts

import (
	"context"
	"database/sql"
	"errors"

	"github.com/jeiang/atrium/internal/db"
)

func Exists(ctx context.Context, q db.Querier, name string) (bool, error) {
	var one int
	err := q.QueryRowContext(ctx, "SELECT 1 FROM accounts WHERE name = ?", name).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func ExistsAny(ctx context.Context, q db.Querier) (bool, error) {
	var one int
	err := q.QueryRowContext(ctx, "SELECT 1 FROM accounts LIMIT 1").Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

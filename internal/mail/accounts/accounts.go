package accounts

import (
	"context"
	"database/sql"
	"errors"

	"github.com/jeiang/atrium/internal/config"
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

func Existing(ctx context.Context, q db.Querier) ([]config.AccountSpec, error) {
	var out []config.AccountSpec
	for _, spec := range config.From(ctx).Accounts() {
		found, err := Exists(ctx, q, spec.Name)
		if err != nil {
			return nil, err
		}
		if !found {
			continue
		}
		out = append(out, spec)
	}
	return out, nil
}

package integrations

import (
	"context"
	"database/sql"
	"errors"

	"github.com/jeiang/atrium/internal/db"
)

func Health(ctx context.Context, q db.Querier, name string) (state, reason string, err error) {
	err = q.QueryRowContext(ctx, "SELECT state, reason FROM integration_health WHERE name = ?", name).Scan(&state, &reason)
	if errors.Is(err, sql.ErrNoRows) {
		return Untested, "", nil
	}
	return state, reason, err
}

func Delete(ctx context.Context, q db.Querier, name string) error {
	_, err := q.ExecContext(ctx, "DELETE FROM integration_health WHERE name = ?", name)
	return err
}

package usage

import (
	"context"
	"database/sql"
	"errors"

	"github.com/jeiang/atrium/internal/db"
)

func HasRows(ctx context.Context, q db.Querier) (bool, error) {
	var one int
	err := q.QueryRowContext(ctx, "SELECT 1 FROM usage LIMIT 1").Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

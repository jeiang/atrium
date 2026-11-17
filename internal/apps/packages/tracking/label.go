package tracking

import (
	"context"

	"github.com/jeiang/atrium/internal/db"
)

func Label(ctx context.Context, q db.Querier, id int64) (string, error) {
	var label string
	err := q.QueryRowContext(ctx,
		"SELECT CASE WHEN merchant <> '' THEN merchant WHEN carrier <> '' THEN carrier ELSE 'Package' END FROM packages WHERE id = ?", id).Scan(&label)
	return label, err
}

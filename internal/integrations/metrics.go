package integrations

import (
	"context"
	"database/sql"

	"github.com/jeiang/atrium/internal/db"
)

type HealthState struct {
	Name  string
	State string
}

func HealthStates(ctx context.Context, q db.Querier) ([]HealthState, error) {
	rows, err := q.QueryContext(ctx, "SELECT name, state FROM integration_health ORDER BY name")
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (HealthState, error) {
		var h HealthState
		err := r.Scan(&h.Name, &h.State)
		return h, err
	})
}

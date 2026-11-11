package mail

import (
	"context"
	"database/sql"

	"github.com/jeiang/atrium/internal/db"
)

type Probe struct {
	Kind      string
	OK        int64
	ProbedAt  float64
	Dimension int64
}

func Probes(ctx context.Context, q db.Querier) ([]Probe, error) {
	rows, err := q.QueryContext(ctx, "SELECT kind, ok, probed_at, COALESCE(dimension, 0) FROM endpoint_probes ORDER BY kind")
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (Probe, error) {
		var p Probe
		err := r.Scan(&p.Kind, &p.OK, &p.ProbedAt, &p.Dimension)
		return p, err
	})
}

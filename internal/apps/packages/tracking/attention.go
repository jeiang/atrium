package tracking

import (
	"context"
	"database/sql"

	"github.com/jeiang/atrium/internal/db"
)

type AttentionPackage struct {
	ID             int64
	Merchant       string
	Status         string
	TrackingNumber string
	FirstSeen      float64
}

func AttentionPackages(ctx context.Context, q db.Querier) ([]AttentionPackage, error) {
	rows, err := q.QueryContext(ctx,
		"SELECT id, merchant, COALESCE(NULLIF(last_event, ''), status_text), tracking_number, first_seen FROM packages "+
			"WHERE dismissed = 0 AND status NOT IN ('delivered', 'returned') ORDER BY first_seen")
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (AttentionPackage, error) {
		var p AttentionPackage
		err := r.Scan(&p.ID, &p.Merchant, &p.Status, &p.TrackingNumber, &p.FirstSeen)
		return p, err
	})
}

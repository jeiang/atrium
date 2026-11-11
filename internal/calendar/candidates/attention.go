package candidates

import (
	"context"
	"database/sql"

	"github.com/jeiang/atrium/internal/db"
)

type PendingItem struct {
	MessageID int64
	Reason    string
	TS        float64
}

func PendingItems(ctx context.Context, q db.Querier) ([]PendingItem, error) {
	rows, err := q.QueryContext(ctx, "SELECT message_id, reason, ts FROM event_candidates WHERE status = 'pending' ORDER BY ts")
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (PendingItem, error) {
		var item PendingItem
		err := r.Scan(&item.MessageID, &item.Reason, &item.TS)
		return item, err
	})
}

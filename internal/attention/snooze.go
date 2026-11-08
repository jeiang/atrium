package attention

import (
	"context"

	"github.com/jeiang/atrium/internal/db"
)

func Snooze(ctx context.Context, q db.Querier, kind, sourceID string, until, now float64) error {
	_, err := q.ExecContext(ctx,
		"INSERT INTO attention_snoozes (kind, source_id, until, created_ts) VALUES (?, ?, ?, ?) "+
			"ON CONFLICT(kind, source_id) DO UPDATE SET until = excluded.until, created_ts = excluded.created_ts",
		kind, sourceID, until, now)
	return err
}

func Unsnooze(ctx context.Context, q db.Querier, kind, sourceID string) error {
	_, err := q.ExecContext(ctx, "DELETE FROM attention_snoozes WHERE kind = ? AND source_id = ?", kind, sourceID)
	return err
}

func snoozed(ctx context.Context, q db.Querier, now float64) (map[string]bool, error) {
	rows, err := q.QueryContext(ctx, "SELECT kind, source_id FROM attention_snoozes WHERE until > ?", now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	hidden := map[string]bool{}
	for rows.Next() {
		var kind, sourceID string
		if err := rows.Scan(&kind, &sourceID); err != nil {
			return nil, err
		}
		hidden[kind+"\x00"+sourceID] = true
	}
	return hidden, rows.Err()
}

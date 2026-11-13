package progress

import (
	"context"
	"database/sql"
	"time"
)

const Retention = 30 * 24 * time.Hour

func Prune(ctx context.Context, tx *sql.Tx, now float64) (int64, error) {
	cutoff := now - Retention.Seconds()
	const expired = "SELECT s.run_id FROM run_streams s JOIN runs r ON r.id = s.run_id WHERE s.terminal_at < ? AND s.retained_after < s.terminal_seq AND r.state IN ('done', 'failed')"
	if _, err := tx.ExecContext(ctx, "DELETE FROM run_events WHERE stream_run_id IN ("+expired+")", cutoff); err != nil {
		return 0, err
	}
	res, err := tx.ExecContext(ctx, "UPDATE run_streams SET retained_after = terminal_seq WHERE run_id IN ("+expired+")", cutoff)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

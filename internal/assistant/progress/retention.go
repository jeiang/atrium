package progress

import (
	"context"
	"database/sql"
	"time"

	"github.com/jeiang/atrium/internal/db"
)

const (
	Retention  = 30 * 24 * time.Hour
	pruneBatch = 100
	expired    = "SELECT s.run_id FROM run_streams s JOIN runs r ON r.id = s.run_id WHERE s.terminal_at < ? AND s.retained_after < s.terminal_seq AND r.state IN ('done', 'failed')"
)

func Prune(ctx context.Context, d *db.DB, now float64) (int64, error) {
	cutoff := now - Retention.Seconds()
	var pruned int64
	for {
		rows, err := d.Read.QueryContext(ctx, expired+" LIMIT ?", cutoff, pruneBatch)
		if err != nil {
			return pruned, err
		}
		ids, err := db.Collect(rows, func(r *sql.Rows) (int64, error) { var id int64; err := r.Scan(&id); return id, err })
		if err != nil || len(ids) == 0 {
			return pruned, err
		}
		before := pruned
		for _, id := range ids {
			err := d.WithTx(ctx, func(tx *sql.Tx) error {
				done, err := db.Changed(tx.ExecContext(ctx, "UPDATE run_streams SET retained_after = terminal_seq WHERE run_id = ? AND run_id IN ("+expired+")", id, cutoff))
				if err != nil || !done {
					return err
				}
				pruned++
				_, err = tx.ExecContext(ctx, "DELETE FROM run_events WHERE stream_run_id = ?", id)
				return err
			})
			if err != nil {
				return pruned, err
			}
		}
		if pruned == before {
			return pruned, nil
		}
	}
}

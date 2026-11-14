package proposals

import (
	"context"
	"database/sql"
	"errors"

	"github.com/jeiang/atrium/internal/db"
)

func DismissKind(ctx context.Context, q db.Querier, kind string, ts float64) error {
	_, err := q.ExecContext(ctx, "UPDATE proposals SET status = 'dismissed', decided_ts = ? WHERE kind = ? AND status = 'pending'", ts, kind)
	return err
}

func IDByFingerprint(ctx context.Context, q db.Querier, fingerprint string) (int64, error) {
	var id int64
	err := q.QueryRowContext(ctx, "SELECT id FROM proposals WHERE fingerprint = ?", fingerprint).Scan(&id)
	return id, err
}

func NeedsCount(ctx context.Context, q db.Querier) (int, error) {
	var pending int
	if err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM proposals WHERE status = 'pending'").Scan(&pending); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil
		}
		return 0, err
	}
	ids, err := blockedIDs(ctx, q)
	if err != nil || len(ids) == 0 {
		return pending, err
	}
	var blocked int
	err = q.QueryRowContext(ctx, "SELECT COUNT(*) FROM proposals WHERE status <> 'pending' AND id IN ("+
		placeholders(len(ids))+")", int64Args(ids)...).Scan(&blocked)
	return pending + blocked, err
}

package proposals

import (
	"context"
	"database/sql"
	"errors"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/ledger"
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
	var n int
	err := q.QueryRowContext(ctx,
		"SELECT (SELECT COUNT(*) FROM proposals WHERE status = 'pending') + "+
			"(SELECT COUNT(*) FROM proposals p WHERE p.status <> 'pending' AND EXISTS "+
			"(SELECT 1 FROM proposal_actions a WHERE a.proposal_id = p.id AND "+ledger.BlockedSQL("a")+"))").Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return n, err
}

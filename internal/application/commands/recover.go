package commands

import (
	"context"
	"database/sql"
)

func (s *Store) Recover(ctx context.Context) (int64, error) {
	var n int64
	err := s.db.WithTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "UPDATE commands SET state = ?, ended_at = ? WHERE state = ?", string(Interrupted), seconds(s.now()), string(Running))
		if err != nil {
			return err
		}
		n, err = res.RowsAffected()
		return err
	})
	return n, err
}

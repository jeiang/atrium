package db

import (
	"context"
	"database/sql"
)

const (
	unknownText = "cancelled, write outcome unknown"
	partialText = "copied but not expunged: "
	heldText    = unknownText + ": write in flight"
	settledText = "settled by operator after checking the mailbox"
	stuckText   = "interrupted before completion"
)

func ledgerState(ctx context.Context, tx *sql.Tx) error {
	for _, c := range []struct{ table, column, ddl string }{
		{"proposal_actions", "state", "TEXT NOT NULL DEFAULT ''"},
		{"proposal_actions", "lease", "REAL NOT NULL DEFAULT 0"},
		{"proposal_actions", "kind", "TEXT NOT NULL DEFAULT 'write'"},
		{"proposal_actions", "undo_of", "INTEGER"},
		{"proposals", "undone_ts", "REAL"},
		{"proposals", "recovered", "INTEGER NOT NULL DEFAULT 0"},
		{"auto_actions", "state", "TEXT NOT NULL DEFAULT ''"},
	} {
		if err := addColumn(ctx, tx, c.table, c.column, c.ddl); err != nil {
			return err
		}
	}
	proposalOK, err := hasColumn(ctx, tx, "proposal_actions", "ok")
	if err != nil {
		return err
	}
	if proposalOK {
		if _, err := tx.ExecContext(ctx, "UPDATE proposal_actions SET "+
			"kind = CASE WHEN json_extract(action, '$.op') = 'undo' THEN 'undo' ELSE 'write' END, "+
			"undo_of = CASE WHEN json_extract(action, '$.op') = 'undo' THEN json_extract(action, '$.of') END, "+
			"lease = ts, "+
			"state = CASE "+
			"WHEN ok = 1 THEN 'ok' "+
			"WHEN error = ? THEN 'held' "+
			"WHEN instr(error, ?) > 0 THEN 'unknown' "+
			"WHEN instr(error, ?) > 0 THEN 'partial' "+
			"WHEN error = ? THEN 'settled' "+
			"ELSE 'failed' END "+
			"WHERE state = ''", heldText, unknownText, partialText, settledText); err != nil {
			return err
		}
	}
	autoOK, err := hasColumn(ctx, tx, "auto_actions", "ok")
	if err != nil {
		return err
	}
	if autoOK {
		if _, err := tx.ExecContext(ctx, "UPDATE auto_actions SET state = CASE "+
			"WHEN undone_ts IS NOT NULL THEN 'undone' "+
			"WHEN undone_error = ? THEN 'undo_held' "+
			"WHEN undone_error = ? THEN 'undo_settled' "+
			"WHEN instr(undone_error, ?) > 0 THEN 'undo_unknown' "+
			"WHEN instr(undone_error, ?) > 0 THEN 'undo_partial' "+
			"WHEN undone_error IS NOT NULL THEN 'undo_failed' "+
			"WHEN ok IS NULL AND error = ? THEN 'interrupted' "+
			"WHEN ok IS NULL THEN 'held' "+
			"WHEN ok = 1 THEN 'applied' "+
			"WHEN error LIKE 'skipped:%' THEN 'skipped' "+
			"WHEN instr(error, ?) > 0 THEN 'unknown' "+
			"WHEN instr(error, ?) > 0 THEN 'partial' "+
			"ELSE 'failed' END "+
			"WHERE state = ''", heldText, settledText, unknownText, partialText, stuckText, unknownText, partialText); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, "UPDATE proposals SET "+
		"undone_ts = COALESCE(json_extract(result, '$.undone_ts'), undone_ts), "+
		"recovered = CASE WHEN json_extract(result, '$.interrupted') THEN 1 ELSE recovered END "+
		"WHERE result IS NOT NULL"); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DROP INDEX IF EXISTS auto_actions_once"); err != nil {
		return err
	}
	for _, table := range []string{"proposal_actions", "auto_actions"} {
		ok, err := hasColumn(ctx, tx, table, "ok")
		if err != nil {
			return err
		}
		if ok {
			if _, err := tx.ExecContext(ctx, "ALTER TABLE "+table+" DROP COLUMN ok"); err != nil {
				return err
			}
		}
	}
	_, err = tx.ExecContext(ctx, "CREATE UNIQUE INDEX IF NOT EXISTS auto_actions_once ON auto_actions "+
		"(message_id, kind, COALESCE(dest, '')) WHERE state IN ('held', 'interrupted', 'applied', 'undo_held', 'undo_failed', 'undo_unknown', 'undo_partial', 'undo_settled')")
	return err
}

func hasColumn(ctx context.Context, tx *sql.Tx, table, column string) (bool, error) {
	var n int
	err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?", table, column).Scan(&n)
	return n > 0, err
}

func addColumn(ctx context.Context, tx *sql.Tx, table, column, ddl string) error {
	ok, err := hasColumn(ctx, tx, table, column)
	if err != nil || ok {
		return err
	}
	_, err = tx.ExecContext(ctx, "ALTER TABLE "+table+" ADD COLUMN "+column+" "+ddl)
	return err
}

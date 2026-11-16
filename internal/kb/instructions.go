package kb

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/settings"
	"github.com/jeiang/atrium/internal/settings/audit"
)

const (
	InstructionsOrigin = "atrium:instructions"
	InstructionsTitle  = "Standing instructions"
	InstructionsHeader = "Standing instructions from the operator:"
	InstructionsCarry  = "prompt_system"
)

func (r Record) StandingInstructions() bool {
	return r.Origin != nil && *r.Origin == InstructionsOrigin
}

func InstructionsID(ctx context.Context, q db.Querier) (int64, error) {
	var id int64
	err := q.QueryRowContext(ctx, "SELECT id FROM kb_records WHERE origin = ?", InstructionsOrigin).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return id, err
}

func InstructionsBlock(ctx context.Context, q db.Querier, projectID int64) (string, error) {
	clause, args := privateClause("r", projectID)
	var body string
	err := q.QueryRowContext(ctx, "SELECT r.body"+fromClause()+" WHERE r.origin = ? AND r.retracted_at IS NULL AND "+clause,
		append([]any{InstructionsOrigin}, args...)...).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if body = strings.TrimSpace(body); body == "" {
		return "", nil
	}
	return InstructionsHeader + "\n" + body, nil
}

func EnsureInstructions(ctx context.Context, d *db.DB, compiled string, now float64) error {
	return d.WithTx(ctx, func(tx *sql.Tx) error {
		if id, err := InstructionsID(ctx, tx); err != nil || id != 0 {
			return err
		}
		stored, ok, err := settings.StoredValue(ctx, tx, InstructionsCarry)
		if err != nil {
			return err
		}
		body := ""
		if text := strings.TrimSpace(stored); ok && text != "" && text != strings.TrimSpace(compiled) {
			body = stored
		}
		aliases, err := aliasesJSON(nil)
		if err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx,
			"INSERT INTO kb_records (title, aliases, body, scope, private, marks, writer, origin, pinned, confirmed_at, created_at, updated_at) "+
				"VALUES (?, ?, ?, ?, 0, 0, ?, ?, 1, ?, ?, ?) ON CONFLICT(origin) DO NOTHING",
			InstructionsTitle, aliases, body, ScopePersonal, WriterOperator, InstructionsOrigin, now, now, now)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil || n == 0 || body == "" {
			return err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		note := "carried to knowledge record " + Cite(id)
		return audit.Record(audit.With(ctx, "atrium", audit.Migration), tx, audit.Entry{Key: InstructionsCarry, New: &note})
	})
}

func notInstructions(alias string) string {
	return "(" + alias + ".origin IS NULL OR " + alias + ".origin != '" + InstructionsOrigin + "')"
}

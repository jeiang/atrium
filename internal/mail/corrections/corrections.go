// Package corrections is src/atrium/corrections.py: the operator's
// destination for a message, which outranks every other routing stage.
package corrections

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/py"
)

const (
	Source         = "operator"
	MaxFolderChars = 200
)

// Error is Python's CorrectionError; its text is shown to the operator.
type Error string

func (e Error) Error() string { return string(e) }

func errorf(format string, args ...any) error { return Error(fmt.Sprintf(format, args...)) }

// Correction is one corrections row.
type Correction struct {
	MessageID int64   `json:"message_id"`
	Account   string  `json:"account"`
	Folder    string  `json:"folder"`
	TS        float64 `json:"ts"`
	Source    string  `json:"source"`
}

// Querier is the read side of *sql.DB and *sql.Tx.
type Querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Get is get_correction: nil when the message has no correction.
func Get(ctx context.Context, q Querier, messageID int64) (*Correction, error) {
	var c Correction
	err := q.QueryRowContext(ctx, "SELECT message_id, account, folder, ts, source FROM corrections WHERE message_id = ?",
		messageID).Scan(&c.MessageID, &c.Account, &c.Folder, &c.TS, &c.Source)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// Record is record_correction: it stores the correction and logs it as a decision in one transaction. ts is
// the time of the correction. source is Source unless the caller is another actor.
func Record(ctx context.Context, d *db.DB, messageID int64, folder, source string, ts float64) (Correction, error) {
	folder = py.Strip(folder)
	if folder == "" || utf8.RuneCountInString(folder) > MaxFolderChars || strings.Contains(folder, "|") || folder == config.Inbox {
		return Correction{}, errorf("invalid destination %s", py.Repr(folder))
	}
	var out Correction
	err := d.WithTx(ctx, func(tx *sql.Tx) error {
		var account string
		var sent int64
		err := tx.QueryRowContext(ctx, "SELECT account, sent FROM messages WHERE id = ?", messageID).Scan(&account, &sent)
		if errors.Is(err, sql.ErrNoRows) {
			return errorf("unknown message %d", messageID)
		}
		if err != nil {
			return err
		}
		if sent != 0 {
			return errorf("cannot correct a sent message")
		}
		if _, err := tx.ExecContext(ctx,
			"INSERT OR REPLACE INTO corrections (message_id, account, folder, ts, source) VALUES (?, ?, ?, ?, ?)",
			messageID, account, folder, ts, source); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO decisions (message_id, account, ts, stage, key, action, dest, confidence, detail) "+
				"VALUES (?, ?, ?, 'correction', ?, 'file', ?, 1.0, ?)",
			messageID, account, ts, source, folder, "{}"); err != nil {
			return err
		}
		out = Correction{messageID, account, folder, ts, source}
		return nil
	})
	return out, err
}

// Clear is clear_correction: whether a correction was removed. A non-empty folder only removes a correction to
// that folder.
func Clear(ctx context.Context, d *db.DB, messageID int64, folder *string) (bool, error) {
	query, args := "DELETE FROM corrections WHERE message_id = ?", []any{messageID}
	if folder != nil {
		query += " AND folder = ?"
		args = append(args, *folder)
	}
	res, err := d.Write.ExecContext(ctx, query, args...)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// List is list_corrections: the newest corrections first; account "" lists every account.
func List(ctx context.Context, q Querier, account string, limit int) ([]Correction, error) {
	query, args := "SELECT message_id, account, folder, ts, source FROM corrections", []any{}
	if account != "" {
		query += " WHERE account = ?"
		args = append(args, account)
	}
	query += " ORDER BY ts DESC LIMIT ?"
	args = append(args, limit)
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	out, err := db.Collect(rows, func(r *sql.Rows) (Correction, error) {
		var c Correction
		err := r.Scan(&c.MessageID, &c.Account, &c.Folder, &c.TS, &c.Source)
		return c, err
	})
	if out == nil {
		out = []Correction{}
	}
	return out, err
}

// DefaultListLimit is list_corrections' default limit.
const DefaultListLimit = 100

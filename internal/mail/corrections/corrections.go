package corrections

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/mail/message"
	"github.com/jeiang/atrium/internal/py"
)

const (
	Source         = "operator"
	MaxFolderChars = 200
)

type Error string

func (e Error) Error() string { return string(e) }

func errorf(format string, args ...any) error { return Error(fmt.Sprintf(format, args...)) }

type Correction struct {
	MessageID int64   `json:"message_id"`
	Account   string  `json:"account"`
	Folder    string  `json:"folder"`
	TS        float64 `json:"ts"`
	Source    string  `json:"source"`
}

type Querier = db.Querier

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

func Record(ctx context.Context, d *db.DB, messageID int64, folder, source string, ts float64) (Correction, error) {
	folder = py.Strip(folder)
	if folder == "" || utf8.RuneCountInString(folder) > MaxFolderChars || strings.Contains(folder, "|") || folder == config.Inbox {
		return Correction{}, errorf("invalid destination %s", strconv.Quote(folder))
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
		confidence := 1.0
		if err := message.RecordDecision(ctx, tx, message.DecisionRecord{
			MessageID: messageID, Account: account, TS: ts, Stage: "correction", Key: &source, Action: "file", Dest: &folder,
			Confidence: &confidence, Detail: map[string]any{},
		}); err != nil {
			return err
		}
		out = Correction{messageID, account, folder, ts, source}
		return nil
	})
	return out, err
}

type Execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func Clear(ctx context.Context, x Execer, messageID int64, folder *string) (bool, error) {
	query, args := "DELETE FROM corrections WHERE message_id = ?", []any{messageID}
	if folder != nil {
		query += " AND folder = ?"
		args = append(args, *folder)
	}
	res, err := x.ExecContext(ctx, query, args...)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

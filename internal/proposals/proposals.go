package proposals

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/py"
)

const (
	Pending    = "pending"
	Approved   = "approved"
	Dismissed  = "dismissed"
	Applied    = "applied"
	Failed     = "failed"
	Suppressed = "suppressed"
)

type Undoer func(ctx context.Context, plan json.RawMessage) (py.Dict, error)

type SyncHook func(ctx context.Context, account string) error

type Registry struct {
	Executors map[string]Executor
	Undoers   map[string]Undoer
	Sync      SyncHook
}

func NewRegistry() *Registry {
	return &Registry{Executors: map[string]Executor{}, Undoers: map[string]Undoer{}}
}

type Querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func Known(ctx context.Context, q Querier, fingerprint string) (bool, error) {
	var status string
	var unresolved bool
	err := q.QueryRowContext(ctx, "SELECT status, "+unresolvedSQL+" FROM proposals WHERE fingerprint = ?", fingerprint).Scan(&status, &unresolved)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil && (status != Failed || unresolved), err
}

func Create(ctx context.Context, d *db.DB, kind string, account *string, title string, payload, preview any,
	fingerprint string, ts float64) (id int64, ok bool, err error) {
	known, err := Known(ctx, d.Write, fingerprint)
	if err != nil || known {
		return 0, false, err
	}
	body, view := py.JSONDumps(payload), py.JSONDumps(preview)
	err = d.Write.QueryRowContext(ctx, "SELECT id FROM proposals WHERE fingerprint = ?", fingerprint).Scan(&id)
	switch {
	case err == nil:
		_, err = d.Write.ExecContext(ctx, "UPDATE proposals SET kind = ?, account = ?, title = ?, payload = ?, preview = ?, "+
			"created_ts = ?, status = 'pending', decided_ts = NULL, applied_ts = NULL, result = NULL WHERE id = ?",
			kind, account, title, body, view, ts, id)
		return id, err == nil, err
	case !errors.Is(err, sql.ErrNoRows):
		return 0, false, err
	}
	res, err := d.Write.ExecContext(ctx, "INSERT INTO proposals (kind, account, status, title, payload, preview, created_ts, fingerprint) "+
		"VALUES (?, ?, 'pending', ?, ?, ?, ?, ?)", kind, account, title, body, view, ts, fingerprint)
	if err != nil {
		return 0, false, err
	}
	id, err = res.LastInsertId()
	return id, err == nil, err
}

// Package proposals is proposals.py: the human-approved change pipeline. This file holds the undo registry
// shared with live mode and the proposal rows a scanner creates.
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

// Undoer reverses one logged write from its undo plan, the stored JSON object with its "op".
type Undoer func(ctx context.Context, plan json.RawMessage) (py.Dict, error)

// SyncHook is hooks["sync"]: the resync of an account after an applied proposal or an undo.
type SyncHook func(ctx context.Context, account string) error

// Registry is UNDOERS and hooks: the undo handler for each plan op and the post-write sync.
type Registry struct {
	Undoers map[string]Undoer
	Sync    SyncHook
}

func NewRegistry() *Registry { return &Registry{Undoers: map[string]Undoer{}} }

// Querier is the read side of *sql.DB and *sql.Tx.
type Querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Known is known: a proposal with this fingerprint exists and has not failed.
func Known(ctx context.Context, q Querier, fingerprint string) (bool, error) {
	var status string
	err := q.QueryRowContext(ctx, "SELECT status FROM proposals WHERE fingerprint = ?", fingerprint).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil && status != Failed, err
}

// Create is create: a pending proposal, or a failed one with the same fingerprint reopened, created at ts.
// ok is false when the fingerprint is already known. account nil is NULL.
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

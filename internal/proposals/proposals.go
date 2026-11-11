package proposals

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/proposals/actions"
)

const (
	Pending    = "pending"
	Approved   = "approved"
	Dismissed  = "dismissed"
	Applied    = "applied"
	Failed     = "failed"
	Suppressed = "suppressed"
)

type Undoer func(ctx context.Context, plan json.RawMessage) (any, error)

type SyncHook func(ctx context.Context, account string) error

type Kind struct {
	Name     string
	Module   string
	Label    string
	Icon     string
	Picker   bool
	Groups   bool
	Group    bool
	Messages bool
	CountKey string
	Approval bool
}

type DismissHook func(ctx context.Context, p *Proposal) error

type Registry struct {
	Executors  map[string]Executor
	Undoers    map[string]Undoer
	Dismissers map[string]DismissHook
	kinds      []Kind
	byName     map[string]Kind
	Sync       SyncHook
}

func NewRegistry() *Registry {
	return &Registry{Executors: map[string]Executor{}, Undoers: map[string]Undoer{}, Dismissers: map[string]DismissHook{}, byName: map[string]Kind{}}
}

func (r *Registry) AddKind(k Kind) {
	if _, found := r.byName[k.Name]; found {
		panic("proposals: duplicate kind " + k.Name)
	}
	r.byName[k.Name] = k
	r.kinds = append(r.kinds, k)
}

func (r *Registry) Kinds() []Kind { return r.kinds }

func (r *Registry) Kind(name string) (Kind, bool) {
	k, ok := r.byName[name]
	return k, ok
}

func (r *Registry) mustKind(name string) Kind {
	k, _ := r.Kind(name)
	return k
}

func (r *Registry) Known(name string) bool {
	_, ok := r.byName[name]
	return ok
}

type Querier = db.Querier

func Known(ctx context.Context, q Querier, fingerprint string) (bool, error) {
	var status string
	var unresolved bool
	err := q.QueryRowContext(ctx, "SELECT status, "+actions.Unresolved("a")+" FROM proposals WHERE fingerprint = ?", fingerprint).Scan(&status, &unresolved)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil && (status != Failed || unresolved), err
}

var OnCreate func(ctx context.Context, d *db.DB, p *Proposal)

func Create(ctx context.Context, d *db.DB, kind string, account *string, title string, payload, preview any,
	fingerprint string, ts float64) (id int64, created bool, err error) {
	body, err := marshalJSON(payload)
	if err != nil {
		return 0, false, err
	}
	view, err := marshalJSON(preview)
	if err != nil {
		return 0, false, err
	}
	err = d.WithTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "INSERT INTO proposals (kind, account, status, title, payload, preview, created_ts, fingerprint) "+
			"VALUES (?, ?, 'pending', ?, ?, ?, ?, ?) ON CONFLICT(fingerprint) DO NOTHING",
			kind, account, title, body, view, ts, fingerprint)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n == 1 {
			id, err = res.LastInsertId()
			created = true
			return err
		}
		err = tx.QueryRowContext(ctx, "UPDATE proposals SET kind = ?, account = ?, title = ?, payload = ?, preview = ?, "+
			"created_ts = ?, status = 'pending', decided_ts = NULL, applied_ts = NULL, undone_ts = NULL, recovered = 0, result = NULL "+
			"WHERE fingerprint = ? AND status = 'failed' AND NOT "+actions.Unresolved("a")+" RETURNING id",
			kind, account, title, body, view, ts, fingerprint).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			err = tx.QueryRowContext(ctx, "SELECT id FROM proposals WHERE fingerprint = ?", fingerprint).Scan(&id)
		}
		return err
	})
	if err == nil && created && OnCreate != nil {
		if p, getErr := Get(ctx, d.Read, id); getErr == nil && p != nil {
			OnCreate(ctx, d, p)
		}
	}
	return id, created, err
}

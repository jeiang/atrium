package proposals

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/httpx"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/mail/mailops"
	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/safety"
)

var log = logx.Logger("atrium.proposals")

// Statuses is STATUSES, the choices of `proposals list --status`.
var Statuses = [...]string{Pending, Approved, Dismissed, Applied, Failed, Suppressed}

// UndoOp is UNDO_OP: the op of the action that records an undo of an earlier action.
const UndoOp = "undo"

// Executor applies an approved proposal under the operator's selection (nil: everything) and returns its result.
type Executor func(ctx context.Context, p *Proposal, selection any) (py.Dict, error)

// Execer is the write side of *sql.DB and *sql.Tx.
type Execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// Proposal is one proposals row as _row shapes it. Payload is the stored JSON text, nil when the row was listed
// rather than read in full; Preview and Result are decoded (py.JSONLoads values, nil for NULL).
type Proposal struct {
	ID          int64
	Kind        string
	Account     *string
	Status      string
	CreatedTS   float64
	DecidedTS   *float64
	AppliedTS   *float64
	Title       string
	Payload     json.RawMessage
	Preview     any
	Result      any
	Fingerprint string
	Undoable    bool
}

// AccountName is the account, "" for NULL.
func (p *Proposal) AccountName() string {
	if p.Account == nil {
		return ""
	}
	return *p.Account
}

// Dict is the proposal as list and get return it.
func (p *Proposal) Dict() py.Dict {
	out := py.Dict{
		{Key: "id", Value: p.ID}, {Key: "kind", Value: p.Kind}, {Key: "account", Value: nullable(p.Account)},
		{Key: "status", Value: p.Status}, {Key: "created_ts", Value: p.CreatedTS},
		{Key: "decided_ts", Value: nullable(p.DecidedTS)}, {Key: "applied_ts", Value: nullable(p.AppliedTS)},
		{Key: "title", Value: p.Title}, {Key: "preview", Value: p.Preview}, {Key: "result", Value: p.Result},
		{Key: "fingerprint", Value: p.Fingerprint},
	}
	if p.Payload != nil {
		payload, _ := py.JSONLoads(p.Payload)
		out = append(out, py.Pair{Key: "payload", Value: payload})
	}
	return append(out, py.Pair{Key: "undoable", Value: p.Undoable})
}

func (p *Proposal) MarshalJSON() ([]byte, error) { return []byte(py.JSONDumps(p.Dict())), nil }

func nullable[T any](v *T) any {
	if v == nil {
		return nil
	}
	return *v
}

func loads(text *string) (any, error) {
	if text == nil {
		return nil, nil
	}
	return py.JSONLoads([]byte(*text))
}

const columns = "id, kind, account, status, created_ts, decided_ts, applied_ts, title, payload, preview, result, fingerprint"

type scanner interface{ Scan(dest ...any) error }

func scanProposal(s scanner, full bool) (*Proposal, error) {
	var p Proposal
	var payload, preview string
	var result *string
	if err := s.Scan(&p.ID, &p.Kind, &p.Account, &p.Status, &p.CreatedTS, &p.DecidedTS, &p.AppliedTS, &p.Title,
		&payload, &preview, &result, &p.Fingerprint); err != nil {
		return nil, err
	}
	var err error
	if p.Preview, err = py.JSONLoads([]byte(preview)); err != nil {
		return nil, err
	}
	if p.Result, err = loads(result); err != nil {
		return nil, err
	}
	if full {
		if _, err := py.JSONLoads([]byte(payload)); err != nil {
			return nil, err
		}
		p.Payload = json.RawMessage(payload)
	}
	return &p, nil
}

// List is list: newest first, status and kind "" do not filter.
func List(ctx context.Context, q Querier, status, kind string) ([]*Proposal, error) {
	var where []string
	var args []any
	if status != "" {
		where = append(where, "status = ?")
		args = append(args, status)
	}
	if kind != "" {
		where = append(where, "kind = ?")
		args = append(args, kind)
	}
	query := "SELECT " + columns + " FROM proposals"
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	rows, err := q.QueryContext(ctx, query+" ORDER BY created_ts DESC, id DESC", args...)
	if err != nil {
		return nil, err
	}
	out, err := db.Collect(rows, func(r *sql.Rows) (*Proposal, error) { return scanProposal(r, false) })
	if err != nil {
		return nil, err
	}
	for _, p := range out {
		if p.Undoable, err = undoable(ctx, q, p); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Get is get: nil for an unknown id.
func Get(ctx context.Context, q Querier, id int64) (*Proposal, error) {
	p, err := scanProposal(q.QueryRowContext(ctx, "SELECT "+columns+" FROM proposals WHERE id = ?", id), true)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	p.Undoable, err = undoable(ctx, q, p)
	return p, err
}

func undoable(ctx context.Context, q Querier, p *Proposal) (bool, error) {
	if p.Status != Applied {
		return false, nil
	}
	return CanUndo(ctx, q, p.ID)
}

// PendingCount is pending_count.
func PendingCount(ctx context.Context, q Querier) (int64, error) {
	var n int64
	err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM proposals WHERE status = 'pending'").Scan(&n)
	return n, err
}

// CountsByStatus is counts_by_status: the count of each status present, then "all".
func CountsByStatus(ctx context.Context, q Querier) (py.Dict, error) {
	out, err := counts(ctx, q, "SELECT status, COUNT(*) AS n FROM proposals GROUP BY status")
	if err != nil {
		return nil, err
	}
	var all int64
	for _, p := range out {
		all += p.Value.(int64)
	}
	return append(out, py.Pair{Key: "all", Value: all}), nil
}

// CountsByKind is counts_by_kind: the count of each kind with status.
func CountsByKind(ctx context.Context, q Querier, status string) (py.Dict, error) {
	return counts(ctx, q, "SELECT kind, COUNT(*) AS n FROM proposals WHERE status = ? GROUP BY kind", status)
}

func counts(ctx context.Context, q Querier, query string, args ...any) (py.Dict, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	pairs, err := db.Collect(rows, func(r *sql.Rows) (py.Pair, error) {
		var key string
		var n int64
		err := r.Scan(&key, &n)
		return py.Pair{Key: key, Value: n}, err
	})
	return py.Dict(pairs), err
}

// Dismiss is dismiss: an open proposal becomes dismissed, or suppressed when it must never come back.
func Dismiss(ctx context.Context, x Execer, id int64, suppress bool, ts float64) error {
	status := Dismissed
	if suppress {
		status = Suppressed
	}
	res, err := x.ExecContext(ctx, "UPDATE proposals SET status = ?, decided_ts = ? WHERE id = ? AND status IN ('pending', 'failed')",
		status, ts, id)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		if err != nil {
			return err
		}
		return ProposalError(fmt.Sprintf("proposal %d is not open", id))
	}
	return nil
}

// RecordAction is record_action: action and undo are py.JSONDumps values, undo nil for none; errText nil for
// NULL.
func RecordAction(ctx context.Context, x Execer, proposalID int64, action, undo any, ok bool, errText *string, ts float64) (int64, error) {
	var plan any
	if undo != nil {
		plan = py.JSONDumps(undo)
	}
	res, err := x.ExecContext(context.WithoutCancel(ctx), "INSERT INTO proposal_actions (proposal_id, ts, action, undo, ok, error) VALUES (?, ?, ?, ?, ?, ?)",
		proposalID, ts, py.JSONDumps(action), plan, ok, errText)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// Action is one proposal_actions row; Action and Undo are decoded, Undo nil for NULL.
type Action struct {
	ID     int64
	TS     float64
	Action any
	Undo   any
	Plan   json.RawMessage
	OK     bool
	Error  *string
}

func (a Action) Dict() py.Dict {
	return py.Dict{
		{Key: "id", Value: a.ID}, {Key: "ts", Value: a.TS}, {Key: "action", Value: a.Action}, {Key: "undo", Value: a.Undo},
		{Key: "ok", Value: a.OK}, {Key: "error", Value: nullable(a.Error)},
	}
}

func (a Action) MarshalJSON() ([]byte, error) { return []byte(py.JSONDumps(a.Dict())), nil }

func (a Action) field(name string) (any, bool) {
	d, _ := a.Action.(py.Dict)
	return d.Get(name)
}

// Actions is actions: the proposal's actions in the order they were recorded.
func Actions(ctx context.Context, q Querier, proposalID int64) ([]Action, error) {
	rows, err := q.QueryContext(ctx, "SELECT id, ts, action, undo, ok, error FROM proposal_actions WHERE proposal_id = ? ORDER BY id", proposalID)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (Action, error) {
		var a Action
		var action string
		var undo *string
		if err := r.Scan(&a.ID, &a.TS, &action, &undo, &a.OK, &a.Error); err != nil {
			return a, err
		}
		var err error
		if a.Action, err = py.JSONLoads([]byte(action)); err != nil {
			return a, err
		}
		if undo != nil {
			a.Plan = json.RawMessage(*undo)
		}
		a.Undo, err = loads(undo)
		return a, err
	})
}

// undoneIDs is undone_ids: the actions an ok undo action names.
func undoneIDs(logged []Action) map[int64]bool {
	done := map[int64]bool{}
	for _, a := range logged {
		if op, _ := a.field("op"); op == UndoOp && a.OK {
			if of, ok := a.field("of"); ok {
				if id, isInt := of.(int64); isInt {
					done[id] = true
				}
			}
		}
	}
	return done
}

func partialUndo(logged []Action, id int64) bool {
	for _, a := range logged {
		if op, _ := a.field("op"); op != UndoOp || a.OK || a.Error == nil || !mailops.Blocks(*a.Error) {
			continue
		}
		if of, ok := a.field("of"); ok && of == id {
			return true
		}
	}
	return false
}

func pending(logged []Action) []Action {
	done := undoneIDs(logged)
	var todo []Action
	for _, a := range slices.Backward(logged) {
		if a.OK && a.Undo != nil && !done[a.ID] {
			todo = append(todo, a)
		}
	}
	return todo
}

// CanUndo is can_undo: the proposal is applied, not undone, and has an ok action with an undo plan not yet undone.
func CanUndo(ctx context.Context, q Querier, id int64) (bool, error) {
	var status string
	var result *string
	err := q.QueryRowContext(ctx, "SELECT status, result FROM proposals WHERE id = ?", id).Scan(&status, &result)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil || status != Applied {
		return false, err
	}
	loaded, err := loads(result)
	if err != nil {
		return false, err
	}
	if d, isDict := loaded.(py.Dict); isDict {
		if v, _ := d.Get("undone_ts"); py.Truthy(v) {
			return false, nil
		}
	}
	logged, err := Actions(ctx, q, id)
	if err != nil {
		return false, err
	}
	return len(pending(logged)) > 0, nil
}

// Pipeline is approve_and_apply and undo over one database with the registered executors, undoers and sync hook.
// Now is the clock (time.Now when nil).
type Pipeline struct {
	DB       *db.DB
	Registry *Registry
	Now      func() float64
}

func (p *Pipeline) now() float64 {
	if p.Now != nil {
		return p.Now()
	}
	return float64(time.Now().UnixNano()) / 1e9
}

func (p *Pipeline) claim(ctx context.Context, id int64, status string) (bool, error) {
	res, err := p.DB.Write.ExecContext(ctx, "UPDATE proposals SET status = ?, decided_ts = ? WHERE id = ? AND status IN ('pending', 'failed')",
		status, p.now(), id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// sync is _sync: the account's post-write resync; its failure is returned as text, never raised.
func (p *Pipeline) sync(ctx context.Context, prop *Proposal) *string {
	if p.Registry == nil || p.Registry.Sync == nil || prop.Account == nil {
		return nil
	}
	if _, known := config.Account(*prop.Account); !known {
		return nil
	}
	if err := p.Registry.Sync(ctx, *prop.Account); err != nil {
		log.Error(fmt.Sprintf("post-apply sync failed for proposal %d", prop.ID), logx.Err, err.Error())
		text := py.ExceptionText(err)
		return &text
	}
	return nil
}

func scope(ctx context.Context, id int64) context.Context {
	return safety.ApprovedWrites(ctx, strconv.FormatInt(id, 10))
}

// ApproveAndApply is approve_and_apply: the open proposal is claimed and its executor runs as approved proposal
// id. A SelectionError puts it back to pending and is returned; any other failure marks it failed with the error
// as its result. The account is resynced either way. The executor and the resync run on ctx, so a cancelled ctx
// ends the IMAP connection; the ledger and the status updates never run on a cancelled ctx, so the proposal
// ends failed with what was written logged.
func (p *Pipeline) ApproveAndApply(ctx context.Context, id int64, selection any) (*Proposal, error) {
	prop, err := Get(ctx, p.DB.Write, id)
	if err != nil {
		return nil, err
	}
	if prop == nil {
		return nil, ProposalError(fmt.Sprintf("unknown proposal %d", id))
	}
	var executor Executor
	if p.Registry != nil {
		executor = p.Registry.Executors[prop.Kind]
	}
	if executor == nil {
		return nil, ProposalError(fmt.Sprintf("no executor for kind %s", py.Repr(prop.Kind)))
	}
	claimed, err := p.claim(ctx, id, Approved)
	if err != nil {
		return nil, err
	}
	if !claimed {
		return nil, ProposalError(fmt.Sprintf("proposal %d is not open", id))
	}
	settle := context.WithoutCancel(ctx)
	result, err := executor(scope(ctx, id), prop, selection)
	var selErr SelectionError
	switch {
	case errors.As(err, &selErr):
		if _, uerr := p.DB.Write.ExecContext(settle, "UPDATE proposals SET status = 'pending', decided_ts = NULL WHERE id = ?", id); uerr != nil {
			return nil, uerr
		}
		return nil, err
	case err != nil:
		log.Error(fmt.Sprintf("proposal %d failed", id), logx.Err, err.Error())
		failure := py.Dict{{Key: "error", Value: py.ExceptionText(err)}}
		if _, uerr := p.DB.Write.ExecContext(settle, "UPDATE proposals SET status = 'failed', result = ? WHERE id = ?", py.JSONDumps(failure), id); uerr != nil {
			return nil, uerr
		}
		p.sync(ctx, prop)
		return Get(settle, p.DB.Write, id)
	}
	if syncErr := p.sync(ctx, prop); syncErr != nil {
		result = result.Set("sync_error", *syncErr)
	}
	if _, err := p.DB.Write.ExecContext(settle, "UPDATE proposals SET status = 'applied', applied_ts = ?, result = ? WHERE id = ?",
		p.now(), py.JSONDumps(result), id); err != nil {
		return nil, err
	}
	return Get(settle, p.DB.Write, id)
}

// UndoResult is undo's outcome: Undone when every replayed undo succeeded.
type UndoResult struct {
	Undone    bool
	Outcomes  []py.Dict
	SyncError *string
}

func (r UndoResult) Dict() py.Dict {
	outcomes := make([]any, len(r.Outcomes))
	for i, o := range r.Outcomes {
		outcomes[i] = o
	}
	return py.Dict{{Key: "undone", Value: r.Undone}, {Key: "outcomes", Value: outcomes}, {Key: "sync_error", Value: nullable(r.SyncError)}}
}

func (r UndoResult) MarshalJSON() ([]byte, error) { return []byte(py.JSONDumps(r.Dict())), nil }

// Undo is undo: newest first, every ok action with an undo plan not yet undone is replayed by its undoer as
// approved proposal id and logged as an undo action. A failing undoer is logged and the rest still run; the
// proposal is marked undone only when all succeeded. The account is resynced after.
func (p *Pipeline) Undo(ctx context.Context, id int64) (UndoResult, error) {
	prop, err := Get(ctx, p.DB.Write, id)
	if err != nil {
		return UndoResult{}, err
	}
	if prop == nil || prop.Status != Applied {
		return UndoResult{}, ProposalError(fmt.Sprintf("proposal %d is not applied", id))
	}
	logged, err := Actions(ctx, p.DB.Write, id)
	if err != nil {
		return UndoResult{}, err
	}
	approved := scope(ctx, id)
	out := UndoResult{Outcomes: []py.Dict{}}
	settle := context.WithoutCancel(ctx)
	for _, entry := range pending(logged) {
		if partialUndo(logged, entry.ID) {
			return out, ProposalError(fmt.Sprintf("action %d has a partial undo; resolve it in the mailbox before undoing again", entry.ID))
		}
		if err := ctx.Err(); err != nil {
			return out, err
		}
		plan, _ := entry.Undo.(py.Dict)
		op, found := plan.Get("op")
		if !found {
			return UndoResult{}, fmt.Errorf("%w: 'op'", httpx.ErrKey)
		}
		name, isString := op.(string)
		var undoer Undoer
		if isString && p.Registry != nil {
			undoer = p.Registry.Undoers[name]
		}
		if undoer == nil {
			return UndoResult{}, ProposalError("no undo handler for " + py.ReprValue(op))
		}
		detail, err := undoer(approved, entry.Plan)
		if err != nil {
			text := py.ExceptionText(err)
			if ctx.Err() != nil {
				text = mailops.UnknownPrefix + ": " + text
			}
			if _, rerr := RecordAction(ctx, p.DB.Write, id, py.Dict{{Key: "op", Value: UndoOp}, {Key: "of", Value: entry.ID}}, nil, false, &text, p.now()); rerr != nil {
				return UndoResult{}, rerr
			}
			out.Outcomes = append(out.Outcomes, py.Dict{{Key: "action", Value: entry.ID}, {Key: "ok", Value: false}, {Key: "error", Value: err.Error()}})
			continue
		}
		var details any
		if detail != nil {
			details = detail
		}
		if _, err := RecordAction(ctx, p.DB.Write, id, py.Dict{{Key: "op", Value: UndoOp}, {Key: "of", Value: entry.ID}, {Key: "detail", Value: details}}, nil, true, nil, p.now()); err != nil {
			return UndoResult{}, err
		}
		out.Outcomes = append(out.Outcomes, py.Dict{{Key: "action", Value: entry.ID}, {Key: "ok", Value: true}})
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	out.SyncError = p.sync(ctx, prop)
	out.Undone = true
	for _, o := range out.Outcomes {
		if ok, _ := o.Get("ok"); ok == false {
			out.Undone = false
		}
	}
	if out.Undone {
		result, _ := prop.Result.(py.Dict)
		result = result.Set("undone_ts", p.now())
		if _, err := p.DB.Write.ExecContext(settle, "UPDATE proposals SET result = ? WHERE id = ?", py.JSONDumps(result), id); err != nil {
			return UndoResult{}, err
		}
	}
	return out, nil
}

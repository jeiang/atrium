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

var Statuses = [...]string{Pending, Approved, Dismissed, Applied, Failed, Suppressed}

const UndoOp = "undo"

type Executor func(ctx context.Context, p *Proposal, selection any) (py.Dict, error)

type Execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

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

func (p *Proposal) AccountName() string {
	if p.Account == nil {
		return ""
	}
	return *p.Account
}

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
	if p.Status != Applied && p.Status != Failed {
		return false, nil
	}
	return CanUndo(ctx, q, p.ID)
}

func PendingCount(ctx context.Context, q Querier) (int64, error) {
	var n int64
	err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM proposals WHERE status = 'pending'").Scan(&n)
	return n, err
}

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

const InFlightText = mailops.UnknownPrefix + ": write in flight"

func BeginAction(ctx context.Context, x Execer, proposalID int64, action any, ts float64) (int64, error) {
	text := InFlightText
	return RecordAction(ctx, x, proposalID, action, nil, false, &text, ts)
}

func SettleAction(ctx context.Context, x Execer, id int64, action, undo any, ok bool, errText *string, ts float64) error {
	var plan any
	if undo != nil {
		plan = py.JSONDumps(undo)
	}
	_, err := x.ExecContext(context.WithoutCancel(ctx), "UPDATE proposal_actions SET ts = ?, action = ?, undo = ?, ok = ?, error = ? WHERE id = ?",
		ts, py.JSONDumps(action), plan, ok, errText, id)
	return err
}

func DropAction(ctx context.Context, x Execer, id int64) error {
	_, err := x.ExecContext(context.WithoutCancel(ctx), "DELETE FROM proposal_actions WHERE id = ? AND ok = 0 AND error = ?", id, InFlightText)
	return err
}

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

func CanUndo(ctx context.Context, q Querier, id int64) (bool, error) {
	var status string
	var result *string
	err := q.QueryRowContext(ctx, "SELECT status, result FROM proposals WHERE id = ?", id).Scan(&status, &result)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil || (status != Applied && status != Failed) {
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
	if status == Failed && !recovered(logged) {
		return false, nil
	}
	return len(pending(logged)) > 0, nil
}

// recovered reports a proposal that still has a write in flight: a kill, or a ledger write that failed, leaves a row
// nothing settled or dropped, and the batches settled before it stay undoable.
func recovered(logged []Action) bool {
	for _, a := range logged {
		if op, _ := a.field("op"); op != UndoOp && !a.OK && a.Error != nil && *a.Error == InFlightText {
			return true
		}
	}
	return false
}

// StuckSeconds is how long a proposal's newest ledger row must be idle before RecoverStuck treats its in-flight
// write as dead: one IMAP write is bounded by the client's write timeout (2 min), and a live approve touches the
// ledger between its writes. The same bound as drafts.SendingStaleSeconds.
const StuckSeconds = 600

// RecoverStuck fails every approved proposal that has a write in flight and no ledger row newer than StuckSeconds
// before now. The in-flight row stays, so refuseUnresolved and Known keep blocking a retry; the settled batches
// stay undoable.
func RecoverStuck(ctx context.Context, d *db.DB, now float64) (int64, error) {
	failure := py.Dict{{Key: "error", Value: mailops.UnknownPrefix + ": approve interrupted; resolve the mailbox by hand before approving again"}}
	res, err := d.Write.ExecContext(ctx, "UPDATE proposals SET status = ?, result = ? WHERE status = ? AND EXISTS "+
		"(SELECT 1 FROM proposal_actions a WHERE a.proposal_id = proposals.id AND a.ok = 0 AND a.error = ? AND json_extract(a.action, '$.op') IS NOT ?) "+
		"AND NOT EXISTS (SELECT 1 FROM proposal_actions b WHERE b.proposal_id = proposals.id AND b.ts >= ?)",
		Failed, py.JSONDumps(failure), Approved, InFlightText, UndoOp, now-StuckSeconds)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

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

// unresolvedSQL matches a proposal that has a failed write whose outcome is unknown or half done, in the form of
// mailops.Blocks over proposal_actions; an undo's own failure does not count.
var unresolvedSQL = "EXISTS (SELECT 1 FROM proposal_actions a WHERE a.proposal_id = proposals.id AND (" + mailops.BlockingActionSQL("a") + "))"

func (p *Pipeline) claim(ctx context.Context, id int64, status string) (bool, error) {
	res, err := p.DB.Write.ExecContext(ctx, "UPDATE proposals SET status = ?, decided_ts = ? WHERE id = ? AND status IN ('pending', 'failed') AND NOT "+unresolvedSQL,
		status, p.now(), id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

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
		if open, err := Get(ctx, p.DB.Write, id); err == nil && open != nil && (open.Status == Pending || open.Status == Failed) {
			if err := p.refuseUnresolved(ctx, id); err != nil {
				return nil, err
			}
		}
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

func (p *Pipeline) refuseUnresolved(ctx context.Context, id int64) error {
	logged, err := Actions(ctx, p.DB.Write, id)
	if err != nil {
		return err
	}
	for _, a := range logged {
		if op, _ := a.field("op"); op != UndoOp && !a.OK && a.Error != nil && mailops.Blocks(*a.Error) {
			return ProposalError(fmt.Sprintf("action %d has an unknown or partial outcome (%s); resolve it in the mailbox before approving again", a.ID, *a.Error))
		}
	}
	return nil
}

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

func (p *Pipeline) Undo(ctx context.Context, id int64) (UndoResult, error) {
	prop, err := Get(ctx, p.DB.Write, id)
	if err != nil {
		return UndoResult{}, err
	}
	if prop == nil || (prop.Status != Applied && prop.Status != Failed) {
		return UndoResult{}, ProposalError(fmt.Sprintf("proposal %d is not applied", id))
	}
	logged, err := Actions(ctx, p.DB.Write, id)
	if err != nil {
		return UndoResult{}, err
	}
	if prop.Status == Failed && !recovered(logged) {
		return UndoResult{}, ProposalError(fmt.Sprintf("proposal %d is not applied", id))
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
		undoAction := py.Dict{{Key: "op", Value: UndoOp}, {Key: "of", Value: entry.ID}}
		held, err := BeginAction(ctx, p.DB.Write, id, undoAction, p.now())
		if err != nil {
			return UndoResult{}, err
		}
		detail, err := undoer(approved, entry.Plan)
		if err != nil {
			text := py.ExceptionText(err)
			if name == "move_back" {
				text = mailops.UnknownText(err, text)
			}
			if rerr := SettleAction(ctx, p.DB.Write, held, undoAction, nil, false, &text, p.now()); rerr != nil {
				return UndoResult{}, rerr
			}
			out.Outcomes = append(out.Outcomes, py.Dict{{Key: "action", Value: entry.ID}, {Key: "ok", Value: false}, {Key: "error", Value: err.Error()}})
			continue
		}
		var details any
		if detail != nil {
			details = detail
		}
		if err := SettleAction(ctx, p.DB.Write, held, py.Dict{{Key: "op", Value: UndoOp}, {Key: "of", Value: entry.ID}, {Key: "detail", Value: details}}, nil, true, nil, p.now()); err != nil {
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

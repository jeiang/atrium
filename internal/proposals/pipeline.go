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
	"github.com/jeiang/atrium/internal/jsonx"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/mail/mailops"
	"github.com/jeiang/atrium/internal/safety"
)

var log = logx.Logger("atrium.proposals")

var Statuses = [...]string{Pending, Approved, Dismissed, Applied, Failed, Suppressed}

const UndoOp = "undo"

type Executor func(ctx context.Context, p *Proposal, selection any) (any, error)

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
	UndoneTS    *float64
	Recovered   bool
	Title       string
	Payload     json.RawMessage
	Preview     any
	Result      any
	previewRaw  string
	resultRaw   string
	Fingerprint string
	Undoable    bool
	Blocked     bool
	BlockedUndo bool
}

func (p *Proposal) AccountName() string {
	if p.Account == nil {
		return ""
	}
	return *p.Account
}

type jsonFloat float64

func (f jsonFloat) MarshalJSON() ([]byte, error) {
	s := strconv.FormatFloat(float64(f), 'f', -1, 64)
	if !strings.ContainsAny(s, ".eE") {
		s += ".0"
	}
	return []byte(s), nil
}

func jsonFloatPtr(v *float64) *jsonFloat {
	if v == nil {
		return nil
	}
	f := jsonFloat(*v)
	return &f
}

type ProposalView struct {
	ID          int64           `json:"id"`
	Kind        string          `json:"kind"`
	Account     *string         `json:"account"`
	Status      string          `json:"status"`
	CreatedTS   jsonFloat       `json:"created_ts"`
	DecidedTS   *jsonFloat      `json:"decided_ts"`
	AppliedTS   *jsonFloat      `json:"applied_ts"`
	Title       string          `json:"title"`
	Preview     json.RawMessage `json:"preview"`
	Result      json.RawMessage `json:"result"`
	Fingerprint string          `json:"fingerprint"`
	Payload     json.RawMessage `json:"payload,omitempty"`
	Undoable    bool            `json:"undoable"`
}

func (p *Proposal) Dict() ProposalView {
	return ProposalView{
		ID: p.ID, Kind: p.Kind, Account: p.Account, Status: p.Status,
		CreatedTS: jsonFloat(p.CreatedTS), DecidedTS: jsonFloatPtr(p.DecidedTS), AppliedTS: jsonFloatPtr(p.AppliedTS),
		Title: p.Title, Preview: json.RawMessage(p.previewRaw), Result: json.RawMessage(p.resultRaw),
		Fingerprint: p.Fingerprint, Payload: p.Payload, Undoable: p.Undoable,
	}
}

func loads(text *string) (any, error) {
	if text == nil {
		return nil, nil
	}
	return jsonx.Loads([]byte(*text))
}

var columns = "p.id, p.kind, p.account, p.status, p.created_ts, p.decided_ts, p.applied_ts, p.undone_ts, p.recovered, " +
	"p.title, p.payload, p.preview, p.result, p.fingerprint, " +
	"EXISTS (SELECT 1 FROM proposal_actions a WHERE a.proposal_id = p.id AND " + ledger.BlockedSQL("a") + ") AS blocked, " +
	"EXISTS (SELECT 1 FROM proposal_actions a WHERE a.proposal_id = p.id AND a.state IN ('undo_unknown', 'undo_partial')) AS blocked_undo, " +
	"(p.status IN ('applied', 'failed') AND p.undone_ts IS NULL AND (p.status = 'applied' OR p.recovered = 1) AND EXISTS " +
	"(SELECT 1 FROM proposal_actions a WHERE a.proposal_id = p.id AND a.state = 'ok' AND a.undo IS NOT NULL AND NOT EXISTS " +
	"(SELECT 1 FROM proposal_actions b WHERE b.proposal_id = p.id AND b.kind = 'undo' AND b.state = 'ok' AND b.undo_of = a.id))) AS undoable"

type scanner interface{ Scan(dest ...any) error }

func scanProposal(s scanner, full bool) (*Proposal, error) {
	var p Proposal
	var payload, preview string
	var result *string
	var recovered, blocked, blockedUndo, undoable int64
	if err := s.Scan(&p.ID, &p.Kind, &p.Account, &p.Status, &p.CreatedTS, &p.DecidedTS, &p.AppliedTS, &p.UndoneTS, &recovered,
		&p.Title, &payload, &preview, &result, &p.Fingerprint, &blocked, &blockedUndo, &undoable); err != nil {
		return nil, err
	}
	p.Recovered = recovered != 0
	p.Blocked = blocked != 0
	p.BlockedUndo = blockedUndo != 0
	p.Undoable = undoable != 0
	p.previewRaw = preview
	if result != nil {
		p.resultRaw = *result
	}
	var err error
	if p.Preview, err = jsonx.Loads([]byte(preview)); err != nil {
		return nil, err
	}
	if p.Result, err = loads(result); err != nil {
		return nil, err
	}
	if full {
		if _, err := jsonx.Loads([]byte(payload)); err != nil {
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
		where = append(where, "p.status = ?")
		args = append(args, status)
	}
	if kind != "" {
		where = append(where, "p.kind = ?")
		args = append(args, kind)
	}
	query := "SELECT " + columns + " FROM proposals p"
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	rows, err := q.QueryContext(ctx, query+" ORDER BY p.created_ts DESC, p.id DESC", args...)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (*Proposal, error) { return scanProposal(r, false) })
}

func Get(ctx context.Context, q Querier, id int64) (*Proposal, error) {
	p, err := scanProposal(q.QueryRowContext(ctx, "SELECT "+columns+" FROM proposals p WHERE p.id = ?", id), true)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return p, err
}

func ListBlocked(ctx context.Context, q Querier) ([]*Proposal, error) {
	rows, err := q.QueryContext(ctx, "SELECT * FROM (SELECT "+columns+" FROM proposals p WHERE p.status <> 'pending') "+
		"WHERE blocked ORDER BY created_ts DESC, id DESC")
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (*Proposal, error) { return scanProposal(r, false) })
}

func ListUndoable(ctx context.Context, q Querier, filter string, args []any, limit int) ([]*Proposal, error) {
	query := "SELECT * FROM (SELECT " + columns + " FROM proposals p) WHERE undoable"
	if filter != "" {
		query += " AND " + filter
	}
	rows, err := q.QueryContext(ctx, query+" ORDER BY COALESCE(applied_ts, created_ts) DESC, id DESC LIMIT ?", append(args, limit)...)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (*Proposal, error) { return scanProposal(r, true) })
}

const threadRefs = "SELECT m.thread_id AS thread_id, m.id AS message_id, CASE WHEN b.type = 'object' THEN " +
	"CASE WHEN json_extract(b.value, '$.type') = 'tool_result' AND NOT COALESCE(json_extract(b.value, '$.is_error'), 0) " +
	"AND json_valid(json_extract(b.value, '$.content')) THEN json_extract(json_extract(b.value, '$.content'), '$.proposal_id') END " +
	"END AS proposal_id FROM thread_messages m, json_each(m.content_json) b WHERE m.content_json LIKE '%proposal_id%'"

func ThreadOf(ctx context.Context, q Querier, id int64) (int64, bool, error) {
	var thread int64
	err := q.QueryRowContext(ctx, "SELECT thread_id FROM ("+threadRefs+") WHERE proposal_id = ? ORDER BY message_id LIMIT 1", id).Scan(&thread)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	return thread, err == nil, err
}

func PendingInThread(ctx context.Context, q Querier, threadID int64) ([]*Proposal, error) {
	rows, err := q.QueryContext(ctx, "SELECT "+columns+" FROM proposals p WHERE p.status = 'pending' AND p.id IN "+
		"(SELECT proposal_id FROM ("+threadRefs+" AND m.thread_id = ?)) ORDER BY p.created_ts, p.id", threadID)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (*Proposal, error) { return scanProposal(r, false) })
}

func Dismiss(ctx context.Context, d *db.DB, id int64, status string, who Resolver, ts float64) error {
	if status != Dismissed && status != Suppressed {
		return ProposalError("dismiss status must be " + Dismissed + " or " + Suppressed)
	}
	return d.WithTx(ctx, func(tx *sql.Tx) error {
		var blocked bool
		if err := tx.QueryRowContext(ctx, "SELECT "+ledger.UnresolvedSQL("a")+" FROM proposals WHERE id = ?", id).Scan(&blocked); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ProposalError(fmt.Sprintf("unknown proposal %d", id))
			}
			return err
		}
		if blocked {
			return ProposalError(fmt.Sprintf("proposal %d has an unresolved write; resolve it in the mailbox before dismissing", id))
		}
		res, err := tx.ExecContext(ctx, "UPDATE proposals SET status = ?, decided_ts = ? WHERE id = ? AND status IN ('pending', 'failed')",
			status, ts, id)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n == 0 {
			return ProposalError(fmt.Sprintf("proposal %d is not open", id))
		}
		return RecordResolution(ctx, tx, who, ts, ResolveProposalAction, id, &id, status, "")
	})
}

func marshalJSON(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func RecordAction(ctx context.Context, x Execer, proposalID int64, kind string, undoOf *int64, action, undo any, state ledger.State, errText *string, ts float64) (int64, error) {
	var plan any
	if undo != nil {
		text, err := marshalJSON(undo)
		if err != nil {
			return 0, err
		}
		plan = text
	}
	body, err := marshalJSON(action)
	if err != nil {
		return 0, err
	}
	var of any
	if undoOf != nil {
		of = *undoOf
	}
	res, err := x.ExecContext(context.WithoutCancel(ctx), "INSERT INTO proposal_actions "+
		"(proposal_id, ts, lease, kind, undo_of, action, undo, state, error) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
		proposalID, ts, ts, kind, of, body, plan, state, errText)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func BeginAction(ctx context.Context, x Execer, proposalID int64, action any, ts float64) (int64, error) {
	return RecordAction(ctx, x, proposalID, "write", nil, action, nil, ledger.Held, nil, ts)
}

func SettleAction(ctx context.Context, x Execer, id int64, action, undo any, state ledger.State, errText *string, ts float64) error {
	var plan any
	if undo != nil {
		text, err := marshalJSON(undo)
		if err != nil {
			return err
		}
		plan = text
	}
	body, err := marshalJSON(action)
	if err != nil {
		return err
	}
	res, err := x.ExecContext(context.WithoutCancel(ctx), "UPDATE proposal_actions SET ts = ?, lease = ?, action = ?, undo = ?, state = ?, error = ? "+
		"WHERE id = ? AND state = 'held'", ts, ts, body, plan, state, errText, id)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return ErrSettledMeanwhile
	}
	return nil
}

func (p *Pipeline) beginUndo(ctx context.Context, proposalID, of int64, action any) (int64, error) {
	var held int64
	err := p.DB.WithTx(context.WithoutCancel(ctx), func(tx *sql.Tx) error {
		var open bool
		if err := tx.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM proposal_actions a WHERE a.proposal_id = ? AND a.kind = 'undo' "+
			"AND a.undo_of = ? AND a.state IN ('held', 'ok', 'undo_unknown', 'undo_partial'))", proposalID, of).Scan(&open); err != nil {
			return err
		}
		if open {
			return ProposalError(fmt.Sprintf("action %d was already undone or has an undo that is in progress or unresolved; check the mailbox before undoing again", of))
		}
		var err error
		held, err = RecordAction(ctx, tx, proposalID, "undo", &of, action, nil, ledger.Held, nil, p.now())
		return err
	})
	return held, err
}

func DropAction(ctx context.Context, x Execer, id int64) error {
	_, err := x.ExecContext(context.WithoutCancel(ctx), "DELETE FROM proposal_actions WHERE id = ? AND state = 'held' AND error IS NULL", id)
	return err
}

type Action struct {
	ID     int64
	TS     float64
	Lease  float64
	Kind   string
	UndoOf *int64
	Action any
	Undo   any
	State  ledger.State
	Error  *string
}

func Actions(ctx context.Context, q Querier, proposalID int64) ([]Action, error) {
	rows, err := q.QueryContext(ctx, "SELECT id, ts, lease, kind, undo_of, action, undo, state, error FROM proposal_actions WHERE proposal_id = ? ORDER BY id", proposalID)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (Action, error) {
		var a Action
		var action string
		var undo *string
		if err := r.Scan(&a.ID, &a.TS, &a.Lease, &a.Kind, &a.UndoOf, &action, &undo, &a.State, &a.Error); err != nil {
			return a, err
		}
		var err error
		if a.Action, err = jsonx.Loads([]byte(action)); err != nil {
			return a, err
		}
		a.Undo, err = loads(undo)
		return a, err
	})
}

func undoneIDs(logged []Action) map[int64]bool {
	done := map[int64]bool{}
	for _, a := range logged {
		if a.Kind == "undo" && a.State == ledger.OK && a.UndoOf != nil {
			done[*a.UndoOf] = true
		}
	}
	return done
}

func partialUndo(logged []Action, id int64) bool {
	for _, a := range logged {
		if a.Kind != "undo" || !a.State.Blocked() || a.UndoOf == nil {
			continue
		}
		if *a.UndoOf == id {
			return true
		}
	}
	return false
}

func pending(logged []Action) []Action {
	done := undoneIDs(logged)
	var todo []Action
	for _, a := range slices.Backward(logged) {
		if a.State == ledger.OK && a.Undo != nil && !done[a.ID] {
			todo = append(todo, a)
		}
	}
	return todo
}

const (
	StuckSeconds       = 4 * RenewSeconds
	StuckUnheldSeconds = 3600
)

func RecoverStuck(ctx context.Context, d *db.DB, now float64) (int64, error) {
	held := map[string]any{"error": "approve interrupted; resolve the mailbox by hand before approving again"}
	res, err := d.Write.ExecContext(ctx, "UPDATE proposals SET status = ?, result = ?, recovered = 1 WHERE status = ? AND EXISTS "+
		"(SELECT 1 FROM proposal_actions a WHERE a.proposal_id = proposals.id AND a.state = 'held' AND a.kind = 'write') "+
		"AND NOT EXISTS (SELECT 1 FROM proposal_actions b WHERE b.proposal_id = proposals.id AND b.lease >= ?)",
		Failed, jsonx.Dumps(held), Approved, now-StuckSeconds)
	if err != nil {
		return 0, err
	}
	heldN, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	unheld := map[string]any{"error": "approve interrupted before any write was in flight; the settled batches can be undone and the proposal approved again", "interrupted": true}
	res, err = d.Write.ExecContext(ctx, "UPDATE proposals SET status = ?, result = ?, recovered = 1 WHERE status = ? AND decided_ts < ? "+
		"AND NOT EXISTS (SELECT 1 FROM proposal_actions a WHERE a.proposal_id = proposals.id AND (a.state IN ('held', 'unknown', 'partial') OR a.lease >= ?))",
		Failed, jsonx.Dumps(unheld), Approved, now-StuckUnheldSeconds, now-StuckUnheldSeconds)
	if err != nil {
		return heldN, err
	}
	unheldN, err := res.RowsAffected()
	return heldN + unheldN, err
}

type Pipeline struct {
	DB       *db.DB
	Registry *Registry
	Now      func() float64
	Enabled  func(context.Context, db.Querier, string) (bool, error)
}

func (p *Pipeline) enabled(ctx context.Context, kind string) (bool, error) {
	if p.Registry == nil || p.Enabled == nil {
		return true, nil
	}
	k, ok := p.Registry.Kind(kind)
	if !ok || k.Module == "" {
		return true, nil
	}
	return p.Enabled(ctx, p.DB.Read, k.Module)
}

func (p *Pipeline) now() float64 {
	if p.Now != nil {
		return p.Now()
	}
	return float64(time.Now().UnixNano()) / 1e9
}

func (p *Pipeline) claim(ctx context.Context, id int64, status string) (bool, error) {
	res, err := p.DB.Write.ExecContext(ctx, "UPDATE proposals SET status = ?, decided_ts = ? WHERE id = ? AND status IN ('pending', 'failed') AND NOT "+
		ledger.UnresolvedSQL("a"), status, p.now(), id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

type unknownOutcome interface{ OutcomeUnknown() bool }

func outcomeUnknown(err error) bool {
	var unknown unknownOutcome
	if errors.As(err, &unknown) {
		return unknown.OutcomeUnknown()
	}
	return mailops.OutcomeUnknown(err)
}

func (p *Pipeline) sync(ctx context.Context, prop *Proposal) *string {
	if p.Registry == nil || p.Registry.Sync == nil || prop.Account == nil {
		return nil
	}
	if _, known := config.From(ctx).Account(*prop.Account); !known {
		return nil
	}
	if err := p.Registry.Sync(ctx, *prop.Account); err != nil {
		log.Error(fmt.Sprintf("post-apply sync failed for proposal %d", prop.ID), logx.Err, err.Error())
		text := err.Error()
		return &text
	}
	return nil
}

func scope(ctx context.Context, id int64) context.Context {
	return safety.ApprovedWrites(ctx, strconv.FormatInt(id, 10))
}

func (p *Pipeline) ApproveAndApply(ctx context.Context, id int64, selection any, who Resolver) (*Proposal, error) {
	prop, err := Get(ctx, p.DB.Write, id)
	if err != nil {
		return nil, err
	}
	if prop == nil {
		return nil, ProposalError(fmt.Sprintf("unknown proposal %d", id))
	}
	on, err := p.enabled(ctx, prop.Kind)
	if err != nil {
		return nil, err
	}
	if !on {
		return nil, ProposalError(fmt.Sprintf("the %s module is disabled", p.Registry.mustKind(prop.Kind).Module))
	}
	var executor Executor
	if p.Registry != nil {
		executor = p.Registry.Executors[prop.Kind]
	}
	if executor == nil {
		return nil, ProposalError(fmt.Sprintf("no executor for kind %s", strconv.Quote(prop.Kind)))
	}
	claimed, err := p.claim(ctx, id, Approved)
	if err != nil {
		return nil, err
	}
	if !claimed {
		if open, err := Get(ctx, p.DB.Write, id); err == nil && open != nil && (open.Status == Pending || open.Status == Failed) {
			return nil, ProposalError(fmt.Sprintf("proposal %d has an unresolved write; resolve it in the mailbox before approving again", id))
		}
		return nil, ProposalError(fmt.Sprintf("proposal %d is not open", id))
	}
	if err := RecordResolution(ctx, p.DB.Write, who, p.now(), ResolveProposalAction, id, &id, "approved", ""); err != nil {
		return nil, err
	}
	settle := context.WithoutCancel(ctx)
	result, err := executor(scope(ctx, id), prop, selection)
	var selErr SelectionError
	switch {
	case errors.As(err, &selErr):
		if _, uerr := p.DB.Write.ExecContext(settle, "UPDATE proposals SET status = 'pending', decided_ts = NULL, result = NULL, applied_ts = NULL "+
			"WHERE id = ? AND status = 'approved'", id); uerr != nil {
			return nil, uerr
		}
		return nil, err
	case err != nil:
		log.Error(fmt.Sprintf("proposal %d failed", id), logx.Err, err.Error())
		failure, merr := json.Marshal(map[string]any{"error": err.Error()})
		if merr != nil {
			return nil, merr
		}
		res, uerr := p.DB.Write.ExecContext(settle, "UPDATE proposals SET status = 'failed', result = ? WHERE id = ? AND status = 'approved'",
			string(failure), id)
		if uerr != nil {
			return nil, uerr
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return nil, ErrSettledMeanwhile
		}
		p.sync(ctx, prop)
		return Get(settle, p.DB.Write, id)
	}
	if syncErr := p.sync(ctx, prop); syncErr != nil {
		result = withSyncError(result, *syncErr)
	}
	body, merr := json.Marshal(result)
	if merr != nil {
		return nil, merr
	}
	res, err := p.DB.Write.ExecContext(settle, "UPDATE proposals SET status = 'applied', applied_ts = ?, result = ? WHERE id = ? AND status = 'approved'",
		p.now(), string(body), id)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrSettledMeanwhile
	}
	return Get(settle, p.DB.Write, id)
}

type UndoResult struct {
	Undone    bool
	Outcomes  []map[string]any
	SyncError *string
}

type undoView struct {
	Undone    bool    `json:"undone"`
	Outcomes  []any   `json:"outcomes"`
	SyncError *string `json:"sync_error"`
}

func (r UndoResult) Dict() undoView {
	outcomes := make([]any, len(r.Outcomes))
	for i, o := range r.Outcomes {
		outcomes[i] = o
	}
	return undoView{Undone: r.Undone, Outcomes: outcomes, SyncError: r.SyncError}
}

func (p *Pipeline) Undo(ctx context.Context, id int64) (UndoResult, error) {
	prop, err := Get(ctx, p.DB.Write, id)
	if err != nil {
		return UndoResult{}, err
	}
	if prop != nil {
		on, err := p.enabled(ctx, prop.Kind)
		if err != nil {
			return UndoResult{}, err
		}
		if !on {
			return UndoResult{}, ProposalError(fmt.Sprintf("the %s module is disabled", p.Registry.mustKind(prop.Kind).Module))
		}
	}
	if prop == nil || (prop.Status != Applied && prop.Status != Failed) {
		return UndoResult{}, ProposalError(fmt.Sprintf("proposal %d is not applied", id))
	}
	if prop.Status == Failed && !prop.Recovered {
		return UndoResult{}, ProposalError(fmt.Sprintf("proposal %d is not applied", id))
	}
	logged, err := Actions(ctx, p.DB.Write, id)
	if err != nil {
		return UndoResult{}, err
	}
	approved := scope(ctx, id)
	out := UndoResult{Outcomes: []map[string]any{}}
	settle := context.WithoutCancel(ctx)
	for _, entry := range pending(logged) {
		if partialUndo(logged, entry.ID) {
			return out, ProposalError(fmt.Sprintf("action %d has a partial undo; resolve it in the mailbox before undoing again", entry.ID))
		}
		if err := ctx.Err(); err != nil {
			return out, err
		}
		plan, _ := entry.Undo.(map[string]any)
		op, found := plan["op"]
		if !found {
			return out, fmt.Errorf("%w: 'op'", httpx.ErrMissing)
		}
		name, isString := op.(string)
		var undoer Undoer
		if isString && p.Registry != nil {
			undoer = p.Registry.Undoers[name]
		}
		if undoer == nil {
			return out, ProposalError("no undo handler for " + fmt.Sprint(op))
		}
		undoAction := map[string]any{"op": UndoOp, "of": entry.ID}
		held, err := p.beginUndo(ctx, id, entry.ID, undoAction)
		if err != nil {
			return out, err
		}
		detail, err := func() (any, error) {
			defer KeepActionAlive(p.DB.Write, p.now, held)()
			return undoer(approved, json.RawMessage(jsonx.Dumps(entry.Undo)))
		}()
		if err != nil {
			state := ledger.UndoFailed
			if outcomeUnknown(err) {
				state = ledger.UndoUnknown
			} else if _, partial := errors.AsType[*mailops.PartialMove](err); partial {
				state = ledger.UndoPartial
			}
			text := err.Error()
			if rerr := SettleAction(ctx, p.DB.Write, held, undoAction, nil, state, &text, p.now()); rerr != nil {
				return out, rerr
			}
			out.Outcomes = append(out.Outcomes, map[string]any{"action": entry.ID, "ok": false, "error": err.Error()})
			continue
		}
		var details any
		if detail != nil {
			details = detail
		}
		if err := SettleAction(ctx, p.DB.Write, held, map[string]any{"op": UndoOp, "of": entry.ID, "detail": details}, nil, ledger.OK, nil, p.now()); err != nil {
			return out, err
		}
		out.Outcomes = append(out.Outcomes, map[string]any{"action": entry.ID, "ok": true})
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	out.SyncError = p.sync(ctx, prop)
	out.Undone = true
	for _, o := range out.Outcomes {
		if ok, _ := o["ok"]; ok == false {
			out.Undone = false
		}
	}
	if out.Undone {
		if _, err := p.DB.Write.ExecContext(settle, "UPDATE proposals SET undone_ts = ? WHERE id = ?", p.now(), id); err != nil {
			return out, err
		}
	}
	return out, nil
}

func withSyncError(result any, msg string) any {
	switch r := result.(type) {
	case map[string]any:
		r["sync_error"] = msg
		return r
	}
	return result
}

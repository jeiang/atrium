package proposals

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/assistant/journal"
	"github.com/jeiang/atrium/internal/assistant/permission"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/jsonx"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/logx"
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
	ID            int64
	Kind          string
	Account       *string
	Status        string
	CreatedTS     float64
	DecidedTS     *float64
	AppliedTS     *float64
	UndoneTS      *float64
	Recovered     bool
	Title         string
	Payload       json.RawMessage
	Preview       any
	Result        any
	previewRaw    string
	resultRaw     string
	Fingerprint   string
	Undoable      bool
	Blocked       bool
	BlockedEffect bool
	BlockedUndo   bool
	BlockedError  string
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
	"p.title, p.payload, p.preview, p.result, p.fingerprint"

type scanner interface{ Scan(dest ...any) error }

func scanProposal(s scanner, full bool) (*Proposal, error) {
	var p Proposal
	var payload, preview string
	var result *string
	var recovered int64
	if err := s.Scan(&p.ID, &p.Kind, &p.Account, &p.Status, &p.CreatedTS, &p.DecidedTS, &p.AppliedTS, &p.UndoneTS, &recovered,
		&p.Title, &payload, &preview, &result, &p.Fingerprint); err != nil {
		return nil, err
	}
	p.Recovered = recovered != 0
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

func decorate(ctx context.Context, q Querier, props []*Proposal) error {
	if len(props) == 0 {
		return nil
	}
	ids := make([]int64, len(props))
	for i, p := range props {
		ids[i] = p.ID
	}
	flags, err := ledger.ProposalFlags(ctx, q, ids)
	if err != nil {
		return err
	}
	for _, p := range props {
		flag := flags[p.ID]
		p.Blocked = flag.Blocked
		p.BlockedError = flag.BlockedError
		if p.Blocked && p.BlockedError == "" {
			p.BlockedError = UnknownOutcomeText
		}
		p.BlockedEffect = flag.Blocked
		p.BlockedUndo = flag.BlockedUndo
		p.Undoable = (p.Status == Applied || p.Status == Failed) && p.UndoneTS == nil && flag.Undoable
	}
	return nil
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func int64Args(ids []int64) []any {
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	return args
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
	props, err := db.Collect(rows, func(r *sql.Rows) (*Proposal, error) { return scanProposal(r, false) })
	if err != nil {
		return nil, err
	}
	return props, decorate(ctx, q, props)
}

func Get(ctx context.Context, q Querier, id int64) (*Proposal, error) {
	p, err := scanProposal(q.QueryRowContext(ctx, "SELECT "+columns+" FROM proposals p WHERE p.id = ?", id), true)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := decorate(ctx, q, []*Proposal{p}); err != nil {
		return nil, err
	}
	return p, nil
}

func KindOf(ctx context.Context, q Querier, id int64) (string, error) {
	var kind string
	err := q.QueryRowContext(ctx, "SELECT kind FROM proposals WHERE id = ?", id).Scan(&kind)
	return kind, err
}

func blockedIDs(ctx context.Context, q Querier) ([]int64, error) {
	return ledger.BlockedProposalIDs(ctx, q)
}

func undoableIDs(ctx context.Context, q Querier) ([]int64, error) {
	return ledger.UndoableProposalIDs(ctx, q)
}

func unionIDs(a, b []int64) []int64 {
	seen := make(map[int64]struct{}, len(a)+len(b))
	out := make([]int64, 0, len(a)+len(b))
	for _, list := range [][]int64{a, b} {
		for _, id := range list {
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			out = append(out, id)
		}
	}
	return out
}

func ListBlocked(ctx context.Context, q Querier) ([]*Proposal, error) {
	ids, err := blockedIDs(ctx, q)
	if err != nil || len(ids) == 0 {
		return nil, err
	}
	rows, err := q.QueryContext(ctx, "SELECT "+columns+" FROM proposals p WHERE p.status <> 'pending' AND p.id IN ("+
		placeholders(len(ids))+") ORDER BY p.created_ts DESC, p.id DESC", int64Args(ids)...)
	if err != nil {
		return nil, err
	}
	props, err := db.Collect(rows, func(r *sql.Rows) (*Proposal, error) { return scanProposal(r, false) })
	if err != nil {
		return nil, err
	}
	return props, decorate(ctx, q, props)
}

func ListUndoable(ctx context.Context, q Querier, filter string, args []any, limit int) ([]*Proposal, error) {
	ids, err := undoableIDs(ctx, q)
	if err != nil || len(ids) == 0 {
		return nil, err
	}
	query := "SELECT " + columns + " FROM proposals p WHERE p.status IN ('applied','failed') AND p.undone_ts IS NULL AND p.id IN (" +
		placeholders(len(ids)) + ")"
	args = append(int64Args(ids), args...)
	if filter != "" {
		query += " AND " + filter
	}
	rows, err := q.QueryContext(ctx, query+" ORDER BY COALESCE(p.applied_ts, p.created_ts) DESC, p.id DESC LIMIT ?", append(args, limit)...)
	if err != nil {
		return nil, err
	}
	props, err := db.Collect(rows, func(r *sql.Rows) (*Proposal, error) { return scanProposal(r, true) })
	if err != nil {
		return nil, err
	}
	return props, decorate(ctx, q, props)
}

func ThreadOf(ctx context.Context, q Querier, id int64) (int64, bool, error) {
	return journal.ProposalThread(ctx, q, id)
}

func PendingInThread(ctx context.Context, q Querier, threadID int64) ([]*Proposal, error) {
	rows, err := q.QueryContext(ctx, "SELECT "+columns+" FROM proposals p WHERE p.status = 'pending' AND (p.id IN "+
		"(SELECT proposal_id FROM ("+journal.ThreadRefsSQL()+" AND m.thread_id = ?)) OR (p.kind = ? AND json_extract(p.payload, '$.thread_id') = ?)) "+
		"ORDER BY p.created_ts, p.id", threadID, permission.Kind, threadID)
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
		var one int
		if err := tx.QueryRowContext(ctx, "SELECT 1 FROM proposals WHERE id = ?", id).Scan(&one); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ProposalError(fmt.Sprintf("unknown proposal %d", id))
			}
			return err
		}
		blocked, err := ledger.ProposalBlocked(ctx, tx, id)
		if err != nil {
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
		return ledger.AuditProposal(ctx, tx, ts, ledger.Resolver{Actor: who.Actor, Source: who.Source}, id, status)
	})
}

func marshalJSON(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func pendingEffects(effects []ledger.Effect) ([]ledger.Effect, map[int64]bool) {
	active := map[int64]bool{}
	for _, e := range effects {
		if e.Direction == "undo" && e.UndoOf != nil && undoActive(e) {
			active[*e.UndoOf] = true
		}
	}
	var todo []ledger.Effect
	for _, e := range effects {
		if e.Direction != "forward" || !e.Succeeded() {
			continue
		}
		if e.UndoneBy != nil || len(e.Undo) == 0 {
			continue
		}
		todo = append(todo, e)
	}
	return todo, active
}

func undoActive(e ledger.Effect) bool {
	switch e.State {
	case ledger.Intent, ledger.Unknown, ledger.Partial, ledger.Done:
		return e.Resolution == nil || e.Resolution.EffectiveOutcome != "failed"
	}
	return false
}

const (
	RenewSeconds       = 30
	StuckSeconds       = 4 * RenewSeconds
	StuckUnheldSeconds = 3600
)

func RecoverStuck(ctx context.Context, d *db.DB, now float64) (int64, error) {
	held := map[string]any{"error": "approve interrupted; resolve the mailbox by hand before approving again"}
	stale, err := ledger.ProposalsWithStaleIntent(ctx, d.Read, now-StuckSeconds)
	if err != nil {
		return 0, err
	}
	var heldN int64
	if len(stale) > 0 {
		res, err := d.Write.ExecContext(ctx, "UPDATE proposals SET status = ?, result = ?, recovered = 1 WHERE status = ? AND id IN ("+
			placeholders(len(stale))+")", append([]any{Failed, jsonx.Dumps(held), Approved}, int64Args(stale)...)...)
		if err != nil {
			return 0, err
		}
		if heldN, err = res.RowsAffected(); err != nil {
			return 0, err
		}
	}
	store := ledger.NewStore(d, func() float64 { return now })
	if _, err := store.Recover(ctx, now-StuckSeconds); err != nil {
		return heldN, err
	}
	active, err := ledger.ProposalsWithActiveEffects(ctx, d.Read, now-StuckUnheldSeconds)
	if err != nil {
		return heldN, err
	}
	unheld := map[string]any{"error": "approve interrupted before any write was in flight; the settled batches can be undone and the proposal approved again", "interrupted": true}
	query := "UPDATE proposals SET status = ?, result = ?, recovered = 1 WHERE status = ? AND decided_ts < ?"
	args := []any{Failed, jsonx.Dumps(unheld), Approved, now - StuckUnheldSeconds}
	if len(active) > 0 {
		query += " AND id NOT IN (" + placeholders(len(active)) + ")"
		args = append(args, int64Args(active)...)
	}
	res, err := d.Write.ExecContext(ctx, query, args...)
	if err != nil {
		return heldN, err
	}
	unheldN, err := res.RowsAffected()
	return heldN + unheldN, err
}

type Pipeline struct {
	DB       *db.DB
	Ledger   *ledger.Store
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

func (p *Pipeline) Dismiss(ctx context.Context, id int64, status string, who Resolver) error {
	prop, err := Get(ctx, p.DB.Write, id)
	if err != nil {
		return err
	}
	if prop == nil {
		return ProposalError(fmt.Sprintf("unknown proposal %d", id))
	}
	var hook DismissHook
	if p.Registry != nil {
		hook = p.Registry.Dismissers[prop.Kind]
	}
	if hook == nil && prop.Kind == permission.Kind {
		return ProposalError("decide permission requests in the web app")
	}
	if err := Dismiss(ctx, p.DB, id, status, who, p.now()); err != nil {
		return err
	}
	if hook != nil {
		return hook(ctx, prop)
	}
	return nil
}

func (p *Pipeline) claim(ctx context.Context, id int64, status string, who Resolver, check func(context.Context, db.Querier) error) (bool, error) {
	var claimed bool
	err := p.DB.WithTx(ctx, func(tx *sql.Tx) error {
		if check != nil {
			if err := check(ctx, tx); err != nil {
				return err
			}
		}
		blocked, err := ledger.ProposalBlocked(ctx, tx, id)
		if err != nil || blocked {
			return err
		}
		completed, err := ledger.ProposalHasCompletedWork(ctx, tx, id)
		if err != nil {
			return err
		}
		if completed {
			return ProposalError(fmt.Sprintf("proposal %d already has a completed write; undo it before approving again", id))
		}
		res, err := tx.ExecContext(ctx, "UPDATE proposals SET status = ?, decided_ts = ? WHERE id = ? AND status IN ('pending', 'failed')",
			status, p.now(), id)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil || n != 1 {
			return err
		}
		claimed = true
		return ledger.AuditProposal(ctx, tx, p.now(), ledger.Resolver{Actor: who.Actor, Source: who.Source}, id, "approved")
	})
	return claimed, err
}

type unknownOutcome interface{ OutcomeUnknown() bool }

type partialOutcome interface{ Partial() bool }

func outcomeUnknown(err error) bool {
	var unknown unknownOutcome
	return errors.As(err, &unknown) && unknown.OutcomeUnknown()
}

func outcomePartial(err error) bool {
	var partial partialOutcome
	return errors.As(err, &partial) && partial.Partial()
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

func (p *Pipeline) ApproveAndApply(ctx context.Context, id int64, selection any, who Resolver, check func(context.Context, db.Querier) error) (*Proposal, error) {
	prop, err := Get(ctx, p.DB.Write, id)
	if err != nil {
		return nil, err
	}
	if prop == nil {
		return nil, ProposalError(fmt.Sprintf("unknown proposal %d", id))
	}
	if p.Registry != nil && p.Registry.Bind != nil {
		ctx, err = p.Registry.Bind(ctx, prop.Kind)
		if err != nil {
			return nil, err
		}
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
	if locks := p.Registry.mustKind(prop.Kind).Locks; len(locks) > 0 {
		path := config.From(ctx).DBPath()
		if path == "" {
			return nil, errors.New("proposals: no database path for the approval locks")
		}
		set, err := db.ResourceLocks(ctx, path, locks, db.ShortLockWait)
		if err != nil {
			return nil, err
		}
		defer func() { _ = set.Close() }()
		ctx = db.WithHeld(ctx, set)
	}
	claimed, err := p.claim(ctx, id, Approved, who, check)
	if err != nil {
		return nil, err
	}
	if !claimed {
		if open, err := Get(ctx, p.DB.Write, id); err == nil && open != nil && (open.Status == Pending || open.Status == Failed) {
			return nil, ProposalError(fmt.Sprintf("proposal %d has an unresolved write; resolve it in the mailbox before approving again", id))
		}
		return nil, ProposalError(fmt.Sprintf("proposal %d is not open", id))
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
		var refused interface{ MutationRefused() }
		if errors.As(err, &refused) {
			p.sync(settle, prop)
			refreshed, gerr := Get(settle, p.DB.Write, id)
			if gerr != nil {
				return nil, gerr
			}
			return refreshed, err
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

func (p *Pipeline) Undo(ctx context.Context, id int64, who Resolver) (UndoResult, error) {
	prop, err := Get(ctx, p.DB.Write, id)
	if err != nil {
		return UndoResult{}, err
	}
	if prop != nil {
		if p.Registry != nil && p.Registry.Bind != nil {
			ctx, err = p.Registry.Bind(ctx, prop.Kind)
			if err != nil {
				return UndoResult{}, err
			}
		}
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
	effects, err := p.Ledger.ProposalEffects(ctx, id)
	if err != nil {
		return UndoResult{}, err
	}
	todo, active := pendingEffects(effects)
	for _, e := range todo {
		if active[e.ID] {
			return UndoResult{}, ProposalError(fmt.Sprintf("effect %d has an undo in progress or unresolved; resolve it before undoing again", e.ID))
		}
	}
	out := UndoResult{Outcomes: []map[string]any{}}
	settle := context.WithoutCancel(ctx)
	complete := true
	for _, e := range todo {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		undo, err := p.Ledger.Undo(ctx, e.ID, ledger.Resolver{Actor: who.Actor, Source: who.Source})
		if err != nil {
			var refused interface{ MutationRefused() }
			if errors.As(err, &refused) {
				out.SyncError = p.sync(settle, prop)
				return out, err
			}
			out.Outcomes = append(out.Outcomes, map[string]any{"action": e.ID, "ok": false, "error": err.Error()})
			complete = false
			break
		}
		if undo.EffectiveOutcome != string(ledger.Done) {
			text := undo.Error
			if text == "" {
				text = "the undo settled as " + string(undo.State)
			}
			out.Outcomes = append(out.Outcomes, map[string]any{"action": e.ID, "ok": false, "error": text, "state": string(undo.State)})
			complete = false
			break
		}
		out.Outcomes = append(out.Outcomes, map[string]any{"action": e.ID, "ok": true})
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	out.SyncError = p.sync(ctx, prop)
	out.Undone = complete
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

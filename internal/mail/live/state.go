package live

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/endpoint"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/numx"
	"github.com/jeiang/atrium/internal/proposals"
	"github.com/jeiang/atrium/internal/safety"
)

var log = logx.Logger("atrium.live")

const (
	File         = "file"
	Flag         = "flag"
	Trash        = "trash"
	Correct      = "correct"
	SnoozeReturn = "snooze_return"

	GoliveKey   = "golive_ts"
	DeletionKey = "deletion_pass_ts"

	UndoHeldText = "cancelled, write outcome unknown: write in flight"
)

var Kinds = [...]string{File, Flag, Trash, Correct, SnoozeReturn}

const NoNewCorrection = "NOT EXISTS (SELECT 1 FROM corrections c WHERE c.message_id = m.id AND c.ts >= :golive)"

type UndoError string

func (e UndoError) Error() string { return string(e) }

type Querier = db.Querier

type Execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func GetState(ctx context.Context, q Querier, key string) (*string, error) {
	var v string
	err := q.QueryRowContext(ctx, "SELECT value FROM live_state WHERE key = ?", key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &v, nil
}

func SetState(ctx context.Context, x Execer, key, value string) error {
	_, err := x.ExecContext(ctx, "INSERT OR REPLACE INTO live_state (key, value) VALUES (?, ?)", key, value)
	return err
}

type Params struct {
	Account  string
	Attempts int
	Golive   float64
}

func (p Params) Args() []any {
	return []any{sql.Named("account", p.Account), sql.Named("attempts", p.Attempts), sql.Named("golive", p.Golive)}
}

func QueryParams(ctx context.Context, q Querier, account string) (Params, error) {
	r := endpoint.Read(ctx, q)
	p := Params{Account: account, Attempts: r.Int(mail.KeyWriteAttempts), Golive: math.Inf(1)}
	if err := r.Err(); err != nil {
		return Params{}, err
	}
	golive, err := GetState(ctx, q, GoliveKey)
	if err != nil || golive == nil || *golive == "" {
		return p, err
	}
	p.Golive, err = strconv.ParseFloat(strings.TrimSpace(*golive), 64)
	return p, err
}

type Live struct {
	DB       *db.DB
	Cfg      config.Config
	Store    *ledger.Store
	Open     mail.OpenFunc
	Registry *proposals.Registry
	Now      func() float64
}

func (l *Live) now() float64 {
	if l.Now != nil {
		return l.Now()
	}
	return mail.WallClock()
}

func (l *Live) store() *ledger.Store {
	if l.Store == nil {
		panic("live: the effect ledger store is not configured")
	}
	return l.Store
}

func (l *Live) TakeBaseline(ctx context.Context) error {
	return l.DB.WithTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "DELETE FROM live_baseline"); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO live_baseline (message_id) SELECT id FROM messages "+
			"WHERE in_inbox = 1 AND seen = 1 AND flagged = 0 AND sent = 0 AND gone_at IS NULL"); err != nil {
			return err
		}
		return SetState(ctx, tx, GoliveKey, numx.FloatRepr(l.now()))
	})
}

func (l *Live) EnsureGolive(ctx context.Context, before func() error) (bool, error) {
	golive, err := GetState(ctx, l.DB.Write, GoliveKey)
	if err != nil || golive != nil {
		return false, err
	}
	if before != nil {
		if err := before(); err != nil {
			return false, err
		}
	}
	return true, l.TakeBaseline(ctx)
}

func (l *Live) Rebaseline(ctx context.Context) (int64, error) {
	if err := l.TakeBaseline(ctx); err != nil {
		return 0, err
	}
	var n int64
	err := l.DB.Write.QueryRowContext(ctx, "SELECT COUNT(*) FROM live_baseline").Scan(&n)
	return n, err
}

type Target struct {
	MessageID   int64
	IMAPName    string
	UIDValidity uint32
	UID         uint32
}

func text(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

func marshal(v any) json.RawMessage {
	if v == nil {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return b
}

func (l *Live) Begin(ctx context.Context, account string, t Target, digest, kind string, src, dest *string,
	before any, decisionID *int64) (ledger.Ticket, bool, error) {
	key := ledger.AutoKey(t.MessageID, kind, text(dest))
	messageID := t.MessageID
	ticket, err := l.store().Begin(ctx, ledger.New{
		Origin: ledger.AutoOrigin, Operation: kind, OperationKey: key,
		Target:    fmt.Sprintf("mail:%s:%d", account, t.MessageID),
		MessageID: &messageID, Account: account, Digest: digest, DecisionID: decisionID,
		Src: text(src), Dest: text(dest), Action: json.RawMessage(`{}`), Before: marshal(before),
		Resources: []ledger.Resource{{Key: key, RetainOnDone: true}},
	}, nil)
	var conflict *ledger.Conflict
	if errors.As(err, &conflict) {
		return ledger.Ticket{}, false, nil
	}
	if err != nil {
		return ledger.Ticket{}, false, err
	}
	return ticket, true, nil
}

func (l *Live) Finish(ctx context.Context, t ledger.Ticket, after, undo any, note *string) error {
	r := ledger.Result{State: ledger.Done, After: marshal(after), Undo: marshal(undo)}
	if note != nil {
		r.Error = *note
	}
	return l.store().Settle(ctx, t, r, nil)
}

func (l *Live) Fail(ctx context.Context, t ledger.Ticket, msg string) error {
	return l.store().Settle(ctx, t, ledger.Result{State: ledger.Failed, Error: msg}, nil)
}

func (l *Live) Unknown(ctx context.Context, t ledger.Ticket, msg string, after, undo any) error {
	return l.store().Settle(ctx, t, ledger.Result{State: ledger.Unknown, Error: msg, After: marshal(after), Undo: marshal(undo)}, nil)
}

func (l *Live) Partial(ctx context.Context, t ledger.Ticket, msg string, after, undo any) error {
	return l.store().Settle(ctx, t, ledger.Result{State: ledger.Partial, Error: msg, After: marshal(after), Undo: marshal(undo)}, nil)
}

func (l *Live) Hold(ctx context.Context, t ledger.Ticket, msg string, after any) error {
	return l.store().Settle(ctx, t, ledger.Result{State: ledger.Unknown, Error: msg, After: marshal(after)}, nil)
}

func (l *Live) Discard(ctx context.Context, t ledger.Ticket, reason string) error {
	return l.store().Settle(ctx, t, ledger.Result{State: ledger.Skipped, Error: "skipped: " + reason}, nil)
}

func (l *Live) Skip(ctx context.Context, account string, messageID int64, digest, kind string, src, dest *string,
	reason string, decisionID *int64) error {
	_, err := l.store().ApplyLocal(ctx, ledger.New{
		Origin: ledger.AutoOrigin, Operation: kind, OperationKey: ledger.AutoKey(messageID, kind, text(dest)),
		Target:    fmt.Sprintf("mail:%s:%d", account, messageID),
		MessageID: &messageID, Account: account, Digest: digest, DecisionID: decisionID,
		Src: text(src), Dest: text(dest), Action: json.RawMessage(`{}`),
	}, func(context.Context, *sql.Tx) (ledger.Result, error) {
		return ledger.Result{State: ledger.Skipped, Error: "skipped: " + reason}, nil
	})
	return err
}

func (l *Live) Recover(ctx context.Context) (int64, error) {
	n, err := l.store().Recover(ctx, l.now()-ledger.StuckSeconds)
	if n > 0 {
		log.Warn(fmt.Sprintf("%d automatic action(s) were interrupted and will not be retried", n))
	}
	return n, err
}

type Action struct {
	ID               int64                    `json:"id"`
	TS               float64                  `json:"ts"`
	Account          string                   `json:"account"`
	MessageID        int64                    `json:"message_id"`
	Digest           string                   `json:"digest"`
	Kind             string                   `json:"kind"`
	Src              *string                  `json:"src"`
	Dest             *string                  `json:"dest"`
	Before           *string                  `json:"before"`
	After            *string                  `json:"after"`
	Undo             *string                  `json:"undo"`
	DecisionID       *int64                   `json:"decision_id"`
	State            ledger.State             `json:"state"`
	Error            *string                  `json:"error"`
	UndoneTS         *float64                 `json:"undone_ts"`
	UndoneError      *string                  `json:"undone_error"`
	UndoStartedAt    *float64                 `json:"undo_started_at"`
	Subject          *string                  `json:"subject"`
	FromAddr         *string                  `json:"from_addr"`
	FromName         *string                  `json:"from_name"`
	Status           string                   `json:"status"`
	Undoable         bool                     `json:"undoable"`
	Blocked          bool                     `json:"blocked"`
	UndoBlocked      bool                     `json:"undo_blocked"`
	Undone           bool                     `json:"undone"`
	Resolved         bool                     `json:"resolved"`
	EffectiveOutcome string                   `json:"effective_outcome,omitempty"`
	Resolution       *ledger.ResolutionRecord `json:"resolution,omitempty"`
}

func textPtr(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}

func rawPtr(v json.RawMessage) *string {
	if len(v) == 0 {
		return nil
	}
	s := string(v)
	return &s
}

func actionOf(e ledger.Effect) Action {
	a := Action{
		ID: e.ID, TS: e.CreatedAt, Account: e.Account, Digest: e.Digest, Kind: e.Operation,
		Src: textPtr(e.Src), Dest: textPtr(e.Dest), Before: rawPtr(e.Before), After: rawPtr(e.After), Undo: rawPtr(e.Undo),
		DecisionID: e.DecisionID, State: e.State, Error: textPtr(e.Error), UndoneTS: e.UndoneAt,
		UndoneError: textPtr(e.UndoError), Status: string(e.State), Undoable: e.Undoable,
		Blocked: e.Blocked, UndoBlocked: e.UndoBlocked, Undone: e.UndoneBy != nil,
		Resolved: e.Resolution != nil, EffectiveOutcome: e.EffectiveOutcome, Resolution: e.Resolution,
	}
	if e.MessageID != nil {
		a.MessageID = *e.MessageID
	}
	if e.UndoState == ledger.Intent {
		lease := e.UndoLeaseAt
		a.UndoStartedAt = &lease
	}
	return a
}

type messageMeta struct {
	subject  *string
	fromAddr *string
	fromName *string
}

func messageMetaFor(ctx context.Context, q Querier, ids []int64) (map[int64]messageMeta, error) {
	out := map[int64]messageMeta{}
	if len(ids) == 0 {
		return out, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := q.QueryContext(ctx, "SELECT id, subject, from_addr, from_name FROM messages WHERE id IN (?"+
		strings.Repeat(",?", len(ids)-1)+")", args...)
	if err != nil {
		return nil, err
	}
	if _, err := db.Collect(rows, func(r *sql.Rows) (struct{}, error) {
		var id int64
		var m messageMeta
		if err := r.Scan(&id, &m.subject, &m.fromAddr, &m.fromName); err != nil {
			return struct{}{}, err
		}
		out[id] = m
		return struct{}{}, nil
	}); err != nil {
		return nil, err
	}
	return out, nil
}

func (l *Live) Get(ctx context.Context, id int64) (*Action, error) {
	e, err := l.store().Get(ctx, id)
	if err != nil {
		var missing *ledger.NotFound
		if errors.As(err, &missing) {
			return nil, nil
		}
		return nil, err
	}
	a := actionOf(e)
	if a.MessageID != 0 {
		meta, err := messageMetaFor(ctx, l.DB.Read, []int64{a.MessageID})
		if err != nil {
			return nil, err
		}
		if m, ok := meta[a.MessageID]; ok {
			a.Subject, a.FromAddr, a.FromName = m.subject, m.fromAddr, m.fromName
		}
	}
	return &a, nil
}

const DefaultLimit = 100

func (l *Live) List(ctx context.Context, account, kind string, limit, offset int) ([]Action, error) {
	effects, err := l.store().List(ctx, ledger.Filter{
		Origin: ledger.AutoOrigin, Account: account, Operation: kind, Direction: "forward", Limit: limit, Offset: offset,
	})
	if err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(effects))
	for _, e := range effects {
		if e.MessageID != nil {
			ids = append(ids, *e.MessageID)
		}
	}
	meta, err := messageMetaFor(ctx, l.DB.Read, ids)
	if err != nil {
		return nil, err
	}
	out := make([]Action, len(effects))
	for i, e := range effects {
		out[i] = actionOf(e)
		if m, ok := meta[out[i].MessageID]; ok {
			out[i].Subject, out[i].FromAddr, out[i].FromName = m.subject, m.fromAddr, m.fromName
		}
	}
	return out, nil
}

type UndoResult struct {
	ID        int64   `json:"id"`
	Undone    bool    `json:"undone"`
	Detail    any     `json:"detail"`
	SyncError *string `json:"sync_error"`
}

func undoError(err error, id int64) error {
	var refused ledger.Refused
	var missing *ledger.NotFound
	var settled *ledger.SettledMeanwhile
	switch {
	case errors.As(err, &refused), errors.As(err, &settled):
		return UndoError(err.Error())
	case errors.As(err, &missing):
		return UndoError(fmt.Sprintf("unknown action %d", id))
	}
	return err
}

func (l *Live) Undo(ctx context.Context, id int64) (UndoResult, error) {
	forward, err := l.store().Get(ctx, id)
	if err != nil {
		return UndoResult{}, undoError(err, id)
	}
	if forward.UndoBlocked {
		return UndoResult{}, UndoError(fmt.Sprintf("action %d has an undo in progress or a partial undo; resolve it in the mailbox before undoing again", id))
	}
	if forward.UndoneBy != nil || (forward.State != ledger.Done && forward.State != ledger.Partial) || len(forward.Undo) == 0 {
		return UndoResult{}, UndoError(fmt.Sprintf("action %d cannot be undone", id))
	}
	undo, err := l.store().Undo(ctx, id, ledger.Resolver{Actor: "operator", Source: "cli"})
	if err != nil {
		return UndoResult{}, undoError(err, id)
	}
	if undo.State != ledger.Done {
		text := undo.Error
		if text == "" {
			text = "the undo did not complete"
		}
		return UndoResult{}, UndoError(text)
	}
	out := UndoResult{ID: id, Undone: true, Detail: json.RawMessage(undo.After)}
	if l.Registry != nil && l.Registry.Sync != nil {
		if err := l.Registry.Sync(ctx, forward.Account); err != nil {
			log.Error(fmt.Sprintf("post-undo sync failed for action %d", id), logx.Err, err.Error())
			text := err.Error()
			out.SyncError = &text
		}
	}
	return out, nil
}

func (l *Live) Paused() bool { return safety.IsPaused(l.Cfg.PausePath()) }

func (l *Live) SetPaused(paused bool) error { return safety.SetPaused(l.Cfg.PausePath(), paused) }

type Status struct {
	Mode     string                      `json:"mode"`
	Paused   bool                        `json:"paused"`
	GoliveTS *string                     `json:"golive_ts"`
	Baseline int64                       `json:"baseline"`
	Actions  map[string]map[string]int64 `json:"actions"`
}

func (l *Live) Status(ctx context.Context) (Status, error) {
	counts, err := ledger.AutoCounts(ctx, l.DB.Read)
	if err != nil {
		return Status{}, err
	}
	golive, err := GetState(ctx, l.DB.Read, GoliveKey)
	if err != nil {
		return Status{}, err
	}
	var baseline int64
	if err := l.DB.Read.QueryRowContext(ctx, "SELECT COUNT(*) FROM live_baseline").Scan(&baseline); err != nil {
		return Status{}, err
	}
	mode, err := mail.Mode(ctx, l.DB.Read)
	if err != nil {
		return Status{}, err
	}
	return Status{Mode: mode, Paused: l.Paused(), GoliveTS: golive, Baseline: baseline, Actions: counts}, nil
}

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
	"time"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/httpx"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/corrections"
	"github.com/jeiang/atrium/internal/mail/mailops"
	"github.com/jeiang/atrium/internal/proposals"
	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/safety"
)

var log = logx.Logger("atrium.live")

const (
	File    = "file"
	Flag    = "flag"
	Trash   = "trash"
	Correct = "correct"

	GoliveKey   = "golive_ts"
	DeletionKey = "deletion_pass_ts"
	Interrupted = "interrupted before completion"

	Applied = "applied"
	Skipped = "skipped"
	Undone  = "undone"
	Held    = "interrupted"
)

var Kinds = [...]string{File, Flag, Trash, Correct}

const NotBlocked = "NOT EXISTS (SELECT 1 FROM auto_actions a WHERE a.account = m.account AND a.digest = m.digest " +
	"AND a.kind IN ('file', 'trash') AND a.ok IS NOT 0) " +
	"AND (SELECT COUNT(*) FROM auto_actions a WHERE a.account = m.account AND a.digest = m.digest " +
	"AND a.kind IN ('file', 'trash') AND a.ok = 0) < :attempts"

const NoNewCorrection = "NOT EXISTS (SELECT 1 FROM corrections c WHERE c.message_id = m.id AND c.ts >= :golive)"

type UndoError string

func (e UndoError) Error() string { return string(e) }

func (UndoError) PyClass() string { return "UndoError" }

func (UndoError) PyModule() string { return "atrium.live" }

type Querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

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
	r := mail.Read(ctx, q)
	p := Params{Account: account, Attempts: r.Int(mail.KeyWriteAttempts), Golive: math.Inf(1)}
	if err := r.Err(); err != nil {
		return Params{}, err
	}
	golive, err := GetState(ctx, q, GoliveKey)
	if err != nil || golive == nil || *golive == "" {
		return p, err
	}
	p.Golive, err = strconv.ParseFloat(py.Strip(*golive), 64)
	return p, err
}

type Live struct {
	DB       *db.DB
	Cfg      config.Config
	Registry *proposals.Registry
	Now      func() float64
}

func (l *Live) now() float64 {
	if l.Now != nil {
		return l.Now()
	}
	return float64(time.Now().UnixNano()) / 1e9
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
		return SetState(ctx, tx, GoliveKey, py.FloatRepr(l.now()))
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

func nullJSON(v any) any {
	if v == nil {
		return nil
	}
	return py.JSONDumps(v)
}

func nullable[T any](v *T) any {
	if v == nil {
		return nil
	}
	return *v
}

func (l *Live) Begin(ctx context.Context, account string, messageID int64, digest, kind string, src, dest *string,
	before any, decisionID *int64) (id int64, ok bool, err error) {
	res, err := l.DB.Write.ExecContext(ctx, "INSERT INTO auto_actions (ts, account, message_id, digest, kind, src, dest, before, decision_id) "+
		"VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)", l.now(), account, messageID, digest, kind, nullable(src), nullable(dest),
		py.JSONDumps(before), nullable(decisionID))
	if db.IsConstraint(err) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	id, err = res.LastInsertId()
	return id, err == nil, err
}

func (l *Live) Finish(ctx context.Context, id int64, after, undo any, note *string) error {
	_, err := l.DB.Write.ExecContext(ctx, "UPDATE auto_actions SET ok = 1, error = ?, after = ?, undo = ? WHERE id = ?",
		nullable(note), nullJSON(after), nullJSON(undo), id)
	return err
}

func (l *Live) Fail(ctx context.Context, id int64, msg string) error {
	_, err := l.DB.Write.ExecContext(ctx, "UPDATE auto_actions SET ok = 0, error = ? WHERE id = ?", msg, id)
	return err
}

func (l *Live) Partial(ctx context.Context, id int64, msg string, after, undo any) error {
	_, err := l.DB.Write.ExecContext(ctx, "UPDATE auto_actions SET ok = 0, error = ?, after = ?, undo = ? WHERE id = ?",
		msg, py.JSONDumps(after), py.JSONDumps(undo), id)
	return err
}

func (l *Live) AbsorbLabelUndo(ctx context.Context, messageID int64, kind string) (add, remove []string, err error) {
	add, remove = []string{}, []string{}
	const where = " WHERE message_id = ? AND kind = ? AND ok = 0 AND undo IS NOT NULL AND undone_ts IS NULL"
	rows, err := l.DB.Write.QueryContext(ctx, "SELECT undo FROM auto_actions"+where+" ORDER BY id", messageID, kind)
	if err != nil {
		return nil, nil, err
	}
	merge := func(into []string, from []string) []string {
		for _, label := range from {
			if !containsString(into, label) {
				into = append(into, label)
			}
		}
		return into
	}
	for rows.Next() {
		var raw string
		var plan struct{ Add, Remove []string }
		if err := rows.Scan(&raw); err != nil {
			_ = rows.Close()
			return nil, nil, err
		}
		if err := json.Unmarshal([]byte(raw), &plan); err != nil {
			_ = rows.Close()
			return nil, nil, err
		}
		add, remove = merge(add, plan.Add), merge(remove, plan.Remove)
	}
	if err := rows.Close(); err != nil {
		return nil, nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	_, err = l.DB.Write.ExecContext(ctx, "UPDATE auto_actions SET undo = NULL"+where, messageID, kind)
	return add, remove, err
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func (l *Live) Hold(ctx context.Context, id int64, msg string, after any) error {
	_, err := l.DB.Write.ExecContext(ctx, "UPDATE auto_actions SET error = ?, after = ? WHERE id = ? AND ok IS NULL",
		msg, nullJSON(after), id)
	return err
}

func (l *Live) Discard(ctx context.Context, id int64) error {
	_, err := l.DB.Write.ExecContext(ctx, "DELETE FROM auto_actions WHERE id = ? AND ok IS NULL AND error IS NULL", id)
	return err
}

func (l *Live) Skip(ctx context.Context, account string, messageID int64, digest, kind string, src, dest *string,
	reason string, decisionID *int64) error {
	_, err := l.DB.Write.ExecContext(ctx, "INSERT INTO auto_actions (ts, account, message_id, digest, kind, src, dest, decision_id, ok, error) "+
		"VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0, ?)", l.now(), account, messageID, digest, kind, nullable(src), nullable(dest),
		nullable(decisionID), "skipped: "+reason)
	return err
}

func (l *Live) Recover(ctx context.Context) (int64, error) {
	res, err := l.DB.Write.ExecContext(ctx, "UPDATE auto_actions SET error = ? WHERE ok IS NULL AND error IS NULL", Interrupted)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if n > 0 {
		log.Warn(fmt.Sprintf("%d automatic action(s) were interrupted and will not be retried", n))
	}
	return n, err
}

func StateOf(ok *int64, undoneTS *float64) string {
	switch {
	case undoneTS != nil:
		return Undone
	case ok != nil && *ok == 1:
		return Applied
	case ok != nil && *ok == 0:
		return Skipped
	}
	return Held
}

type Action struct {
	ID          int64    `json:"id"`
	TS          float64  `json:"ts"`
	Account     string   `json:"account"`
	MessageID   int64    `json:"message_id"`
	Digest      string   `json:"digest"`
	Kind        string   `json:"kind"`
	Src         *string  `json:"src"`
	Dest        *string  `json:"dest"`
	Before      *string  `json:"before"`
	After       *string  `json:"after"`
	Undo        *string  `json:"undo"`
	DecisionID  *int64   `json:"decision_id"`
	OK          *int64   `json:"ok"`
	Error       *string  `json:"error"`
	UndoneTS    *float64 `json:"undone_ts"`
	UndoneError *string  `json:"undone_error"`
	Subject     *string  `json:"subject"`
	FromAddr    *string  `json:"from_addr"`
	FromName    *string  `json:"from_name"`
	Status      string   `json:"status"`
	Undoable    bool     `json:"undoable"`
}

const actionColumns = "SELECT a.id, a.ts, a.account, a.message_id, a.digest, a.kind, a.src, a.dest, a.before, a.after, a.undo, " +
	"a.decision_id, a.ok, a.error, a.undone_ts, a.undone_error, m.subject, m.from_addr, m.from_name FROM auto_actions a " +
	"LEFT JOIN messages m ON m.id = a.message_id"

type scanner interface{ Scan(dest ...any) error }

func scanAction(s scanner) (Action, error) {
	var a Action
	err := s.Scan(&a.ID, &a.TS, &a.Account, &a.MessageID, &a.Digest, &a.Kind, &a.Src, &a.Dest, &a.Before, &a.After, &a.Undo,
		&a.DecisionID, &a.OK, &a.Error, &a.UndoneTS, &a.UndoneError, &a.Subject, &a.FromAddr, &a.FromName)
	a.Status = StateOf(a.OK, a.UndoneTS)
	a.Undoable = a.OK != nil && (*a.OK == 0 || *a.OK == 1) && a.UndoneTS == nil && a.Undo != nil
	return a, err
}

func Get(ctx context.Context, q Querier, id int64) (*Action, error) {
	a, err := scanAction(q.QueryRowContext(ctx, actionColumns+" WHERE a.id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &a, nil
}

func filters(prefix, account, kind string) (string, []any) {
	var clauses []string
	var args []any
	if account != "" {
		clauses = append(clauses, prefix+"account = ?")
		args = append(args, account)
	}
	if kind != "" {
		clauses = append(clauses, prefix+"kind = ?")
		args = append(args, kind)
	}
	if len(clauses) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(clauses, " AND "), args
}

const DefaultLimit = 100

func List(ctx context.Context, q Querier, account, kind string, limit, offset int) ([]Action, error) {
	where, args := filters("a.", account, kind)
	rows, err := q.QueryContext(ctx, actionColumns+where+" ORDER BY a.id DESC LIMIT ? OFFSET ?", append(args, limit, offset)...)
	if err != nil {
		return nil, err
	}
	out, err := db.Collect(rows, func(r *sql.Rows) (Action, error) { return scanAction(r) })
	if out == nil && err == nil {
		out = []Action{}
	}
	return out, err
}

func Count(ctx context.Context, q Querier, account, kind string) (int64, error) {
	where, args := filters("", account, kind)
	var n int64
	err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM auto_actions"+where, args...).Scan(&n)
	return n, err
}

type UndoResult struct {
	ID        int64   `json:"id"`
	Undone    bool    `json:"undone"`
	Detail    py.Dict `json:"detail"`
	SyncError *string `json:"sync_error"`
}

func (l *Live) Undo(ctx context.Context, id int64) (UndoResult, error) {
	var account, kind string
	var messageID int64
	var ok *int64
	var dest, plan *string
	var undoneTS *float64
	var undoneError *string
	err := l.DB.Write.QueryRowContext(ctx, "SELECT account, kind, message_id, ok, dest, undo, undone_ts, undone_error FROM auto_actions WHERE id = ?", id).
		Scan(&account, &kind, &messageID, &ok, &dest, &plan, &undoneTS, &undoneError)
	if errors.Is(err, sql.ErrNoRows) {
		return UndoResult{}, UndoError(fmt.Sprintf("unknown action %d", id))
	}
	if err != nil {
		return UndoResult{}, err
	}
	if ok == nil || (*ok != 0 && *ok != 1) || undoneTS != nil || plan == nil {
		return UndoResult{}, UndoError(fmt.Sprintf("action %d cannot be undone", id))
	}
	if undoneError != nil && mailops.Blocks(*undoneError) {
		return UndoResult{}, UndoError(fmt.Sprintf("action %d has a partial undo (%s); resolve it in the mailbox before undoing again", id, *undoneError))
	}
	parsed, err := py.JSONLoads([]byte(*plan))
	if err != nil {
		return UndoResult{}, err
	}
	fields, _ := parsed.(py.Dict)
	op, found := fields.Get("op")
	if !found {
		return UndoResult{}, fmt.Errorf("%w: 'op'", httpx.ErrKey)
	}
	name, _ := op.(string)
	var undoer proposals.Undoer
	if l.Registry != nil {
		undoer = l.Registry.Undoers[name]
	}
	if _, isString := op.(string); !isString || undoer == nil {
		return UndoResult{}, UndoError("no undo handler for " + py.ReprValue(op))
	}
	settle := context.WithoutCancel(ctx)
	held, err := l.DB.Write.ExecContext(settle, "UPDATE auto_actions SET undone_error = ?, undo_started_at = ? WHERE id = ? AND undone_ts IS NULL AND NOT "+
		mailops.BlockingTextSQL("undone_error"), proposals.InFlightText, l.now(), id)
	if err != nil {
		return UndoResult{}, err
	}
	if n, err := held.RowsAffected(); err != nil {
		return UndoResult{}, err
	} else if n == 0 {
		return UndoResult{}, UndoError(fmt.Sprintf("action %d has an undo in progress or a partial undo; resolve it in the mailbox before undoing again", id))
	}
	detail, err := undoer(safety.ApprovedWrites(ctx, fmt.Sprintf("undo-%d", id)), json.RawMessage(*plan))
	if err != nil {
		text := py.ExceptionText(err)
		if name == OpMoveBack {
			text = mailops.UnknownText(err, text)
		}
		if uerr := l.settleHeld(settle, id, "UPDATE auto_actions SET undone_error = ? WHERE id = ? AND undone_error = ?", text, id, proposals.InFlightText); uerr != nil {
			return UndoResult{}, uerr
		}
		if ctx.Err() != nil {
			return UndoResult{}, err
		}
		return UndoResult{}, UndoError(text)
	}
	err = l.DB.WithTx(settle, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(settle, "UPDATE auto_actions SET undone_ts = ?, undone_error = NULL WHERE id = ? AND undone_error = ?", l.now(), id, proposals.InFlightText)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n == 0 {
			return errSettledMeanwhile(id)
		}
		if kind == Correct {
			_, err = corrections.Clear(settle, tx, messageID, dest)
		}
		return err
	})
	if err != nil {
		return UndoResult{}, err
	}
	out := UndoResult{ID: id, Undone: true, Detail: detail}
	if l.Registry != nil && l.Registry.Sync != nil {
		if err := l.Registry.Sync(ctx, account); err != nil {
			log.Error(fmt.Sprintf("post-undo sync failed for action %d", id), logx.Err, err.Error())
			text := py.ExceptionText(err)
			out.SyncError = &text
		}
	}
	return out, nil
}

func (l *Live) Paused() bool { return safety.IsPaused(l.Cfg.PausePath()) }

func (l *Live) SetPaused(paused bool) error { return safety.SetPaused(l.Cfg.PausePath(), paused) }

func (l *Live) Status(ctx context.Context) (py.Dict, error) {
	counts := py.Dict{}
	rows, err := l.DB.Read.QueryContext(ctx, "SELECT kind, ok, undone_ts IS NOT NULL AS undone, COUNT(*) AS n FROM auto_actions GROUP BY 1, 2, 3")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var kind string
		var ok *int64
		var undone bool
		var n int64
		if err := rows.Scan(&kind, &ok, &undone, &n); err != nil {
			_ = rows.Close()
			return nil, err
		}
		var mark *float64
		if undone {
			mark = new(1.0)
		}
		label := StateOf(ok, mark)
		byState := py.Dict{}
		if existing, found := counts.Get(kind); found {
			byState = existing.(py.Dict)
		}
		total := n
		if prior, found := byState.Get(label); found {
			total += prior.(int64)
		}
		counts = counts.Set(kind, byState.Set(label, total))
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	golive, err := GetState(ctx, l.DB.Read, GoliveKey)
	if err != nil {
		return nil, err
	}
	var baseline int64
	if err := l.DB.Read.QueryRowContext(ctx, "SELECT COUNT(*) FROM live_baseline").Scan(&baseline); err != nil {
		return nil, err
	}
	mode, err := mail.Mode(ctx, l.DB.Read)
	if err != nil {
		return nil, err
	}
	return py.Dict{
		{Key: "mode", Value: mode}, {Key: "paused", Value: l.Paused()}, {Key: "golive_ts", Value: nullable(golive)},
		{Key: "baseline", Value: baseline}, {Key: "actions", Value: counts},
	}, nil
}

func errSettledMeanwhile(id int64) error {
	return UndoError(fmt.Sprintf("action %d was settled by the operator while its undo ran; the recorded outcome stands", id))
}

func (l *Live) settleHeld(ctx context.Context, id int64, query string, args ...any) error {
	res, err := l.DB.Write.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return errSettledMeanwhile(id)
	}
	return nil
}

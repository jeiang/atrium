package journal

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"unicode"
	"unicode/utf8"

	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/assistant/block"
	"github.com/jeiang/atrium/internal/assistant/pricing"
	"github.com/jeiang/atrium/internal/assistant/progress"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/db/store"
)

const (
	Running     = "running"
	Interrupted = "interrupted"
	Parked      = "parked"
	Done        = "done"
	Failed      = "failed"
	Accounting  = "accounting"
)

const (
	Started = "started"
	OK      = "done"
	Error   = "error"
	Unknown = "unknown"
)

const (
	Safe  = "safe"
	Write = "write"
)

const (
	User      = "user"
	Assistant = "assistant"
)

func LeaseSeconds(ctx context.Context, q db.Querier) (float64, error) { return leaseSeconds(ctx, q) }

func leaseSeconds(_ context.Context, _ db.Querier) (float64, error) {
	return assistant.TuningAgentJournalLeaseSeconds, nil
}

func NewOwner() string {
	suffix := make([]byte, 4)
	_, _ = rand.Read(suffix)
	return ProcessPrefix() + hex.EncodeToString(suffix)
}

func ProcessPrefix() string {
	host, _ := os.Hostname()
	return fmt.Sprintf("%s:%d:", host, os.Getpid())
}

func DeadProcess(owner string) string {
	host, rest, ok := strings.Cut(owner, ":")
	if !ok {
		return ""
	}
	pidText, _, ok := strings.Cut(rest, ":")
	if !ok {
		return ""
	}
	pid, err := strconv.Atoi(pidText)
	self, _ := os.Hostname()
	if err != nil || pid <= 0 || host != self || pid == os.Getpid() || !errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
		return ""
	}
	return host + ":" + pidText + ":"
}

type Run struct {
	ID            int64      `json:"id"`
	ThreadID      int64      `json:"thread_id"`
	ParentRunID   *int64     `json:"parent_run_id"`
	Kind          string     `json:"kind"`
	Model         string     `json:"model"`
	Effort        string     `json:"effort"`
	State         string     `json:"state"`
	Error         *string    `json:"error"`
	Steps         int64      `json:"steps"`
	StartedAt     float64    `json:"started_at"`
	EndedAt       *float64   `json:"ended_at"`
	LimitsJSON    *string    `json:"limits_json"`
	Owner         *string    `json:"owner"`
	LeaseUntil    *float64   `json:"lease_until"`
	Superseded    store.Flag `json:"superseded"`
	StopRequested store.Flag `json:"stop_requested"`
}

const runColumns = "id, thread_id, parent_run_id, kind, model, effort, state, error, steps, started_at, ended_at, limits_json, " +
	"owner, lease_until, superseded, stop_requested"

func scanRun(row interface{ Scan(...any) error }) (Run, error) {
	var r Run
	err := row.Scan(&r.ID, &r.ThreadID, &r.ParentRunID, &r.Kind, &r.Model, &r.Effort, &r.State, &r.Error, &r.Steps, &r.StartedAt,
		&r.EndedAt, &r.LimitsJSON, &r.Owner, &r.LeaseUntil, &r.Superseded, &r.StopRequested)
	return r, err
}

func runs(rows *sql.Rows, err error) ([]Run, error) {
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (Run, error) { return scanRun(r) })
}

var ErrRunActive = errors.New("journal: thread already has an active run")

func StartRun(ctx context.Context, d *db.DB, threadID int64, kind, model, effort string, now float64, parentRunID *int64, owner *string) (int64, error) {
	lease, err := leaseSeconds(ctx, d.Read)
	if err != nil {
		return 0, err
	}
	var id int64
	err = d.WithTx(ctx, func(tx *sql.Tx) error {
		active, err := ActiveRun(ctx, tx, threadID)
		if err != nil {
			return err
		}
		if active != nil {
			return ErrRunActive
		}
		res, err := tx.ExecContext(ctx,
			"INSERT INTO runs (thread_id, parent_run_id, kind, model, effort, state, started_at, owner, lease_until) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
			threadID, parentRunID, kind, model, effort, Running, now, owner, now+lease)
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO run_streams(run_id) VALUES(?)", id)
		return err
	})
	return id, err
}

type Limits struct {
	MaxSteps         *int64
	MaxCostUSD       *float64
	SystemExtra      *string
	AllowedTools     []string
	ToolChoice       *string
	Report           json.RawMessage
	WebSearchMaxUses *int64
	Sources          []int64
	ItemKey          string
}

type limitsJSON struct {
	MaxSteps         *int64          `json:"max_steps,omitempty"`
	MaxCostUSD       *float64        `json:"max_cost_usd,omitempty"`
	SystemExtra      *string         `json:"system_extra,omitempty"`
	AllowedTools     *[]string       `json:"allowed_tools,omitempty"`
	ToolChoice       *string         `json:"tool_choice,omitempty"`
	Report           json.RawMessage `json:"report,omitempty"`
	WebSearchMaxUses *int64          `json:"web_search_max_uses,omitempty"`
	Sources          *[]int64        `json:"sources,omitempty"`
	ItemKey          string          `json:"item_key,omitempty"`
}

func (l Limits) wire() limitsJSON {
	w := limitsJSON{MaxSteps: l.MaxSteps, MaxCostUSD: l.MaxCostUSD, SystemExtra: l.SystemExtra, ToolChoice: l.ToolChoice, Report: l.Report, WebSearchMaxUses: l.WebSearchMaxUses, ItemKey: l.ItemKey}
	if l.AllowedTools != nil {
		w.AllowedTools = &l.AllowedTools
	}
	if l.Sources != nil {
		w.Sources = &l.Sources
	}
	return w
}

func SetLimits(ctx context.Context, q db.Querier, runID int64, l Limits) error {
	body, err := json.Marshal(l.wire())
	if err != nil {
		return err
	}
	_, err = q.ExecContext(ctx, "UPDATE runs SET limits_json = ? WHERE id = ?", string(body), runID)
	return err
}

func LimitsOf(ctx context.Context, q db.Querier, runID int64) (Limits, error) {
	var raw *string
	err := q.QueryRowContext(ctx, "SELECT limits_json FROM runs WHERE id = ?", runID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) || err == nil && (raw == nil || *raw == "") {
		return Limits{}, nil
	}
	if err != nil {
		return Limits{}, err
	}
	var stored limitsJSON
	if err := json.Unmarshal([]byte(*raw), &stored); err != nil {
		return Limits{}, fmt.Errorf("journal: run %d limits_json: %w", runID, err)
	}
	out := Limits{MaxSteps: stored.MaxSteps, MaxCostUSD: stored.MaxCostUSD, SystemExtra: stored.SystemExtra, ToolChoice: stored.ToolChoice, Report: stored.Report, WebSearchMaxUses: stored.WebSearchMaxUses, ItemKey: stored.ItemKey}
	if stored.AllowedTools != nil {
		out.AllowedTools = *stored.AllowedTools
	}
	if stored.Sources != nil {
		out.Sources = *stored.Sources
	}
	return out, nil
}

func GetRun(ctx context.Context, q db.Querier, runID int64) (*Run, error) {
	r, err := scanRun(q.QueryRowContext(ctx, "SELECT "+runColumns+" FROM runs WHERE id = ?", runID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

const (
	classificationKind = "classification"
	classificationText = 200
)

func StartClassification(ctx context.Context, d *db.DB, caller, itemKey string, now float64) (runID, threadID int64, err error) {
	if err := classificationCaller(caller); err != nil {
		return 0, 0, err
	}
	if itemKey == "" {
		return 0, 0, errors.New("journal: a classification run needs an item key")
	}
	if err := classificationMeta("item key", itemKey); err != nil {
		return 0, 0, err
	}
	limits, err := json.Marshal(Limits{ItemKey: itemKey}.wire())
	if err != nil {
		return 0, 0, err
	}
	role := classificationKind + ":" + caller
	err = d.WithTx(ctx, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, "SELECT id FROM threads WHERE kind = ? AND role = ? AND deleted_at IS NULL ORDER BY id LIMIT 1",
			classificationKind, role).Scan(&threadID)
		if errors.Is(err, sql.ErrNoRows) {
			res, err := tx.ExecContext(ctx,
				"INSERT INTO threads (kind, title, effort, created_at, updated_at, role, model, thinking, web_search) VALUES (?, ?, '', ?, ?, ?, ?, 'disabled', 0)",
				classificationKind, "Classification: "+caller, now, now, role, pricing.HaikuID)
			if err != nil {
				return err
			}
			if threadID, err = res.LastInsertId(); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx,
			"INSERT INTO runs (thread_id, kind, model, effort, state, started_at, limits_json) VALUES (?, ?, ?, '', ?, ?, ?)",
			threadID, classificationKind, pricing.HaikuID, Accounting, now, string(limits))
		if err != nil {
			return err
		}
		runID, err = res.LastInsertId()
		return err
	})
	if err != nil {
		return 0, 0, err
	}
	return runID, threadID, nil
}

func FinishClassification(ctx context.Context, q db.Querier, runID int64, state string, now float64, reason string) error {
	switch {
	case state != Done && state != Failed:
		return fmt.Errorf("journal: classification run %d cannot finish as %q", runID, state)
	case state == Failed && reason == "":
		return fmt.Errorf("journal: failed classification run %d needs a reason", runID)
	}
	if err := classificationMeta("reason", reason); err != nil {
		return err
	}
	var errText *string
	if reason != "" {
		errText = &reason
	}
	changed, err := db.Changed(q.ExecContext(ctx, "UPDATE runs SET state = ?, error = ?, ended_at = ? WHERE id = ? AND state = ?",
		state, errText, now, runID, Accounting))
	if err != nil {
		return err
	}
	if !changed {
		return fmt.Errorf("journal: run %d is not an open classification run", runID)
	}
	return nil
}

func LatestClassificationError(ctx context.Context, q db.Querier, caller string, since float64) (string, error) {
	var reason string
	err := q.QueryRowContext(ctx,
		"SELECT COALESCE(r.error, '') FROM runs r JOIN threads t ON t.id = r.thread_id "+
			"WHERE t.kind = ? AND t.role = ? AND r.kind = ? AND r.state IN (?, ?) AND r.started_at >= ? ORDER BY r.id DESC LIMIT 1",
		classificationKind, classificationKind+":"+caller, classificationKind, Done, Failed, since).Scan(&reason)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return reason, err
}

func classificationCaller(caller string) error {
	if caller == "" || len(caller) > classificationText {
		return fmt.Errorf("journal: invalid classification caller %q", caller)
	}
	for _, r := range caller {
		if (r < 'a' || r > 'z') && r != '_' {
			return fmt.Errorf("journal: invalid classification caller %q", caller)
		}
	}
	return nil
}

func classificationMeta(what, text string) error {
	if utf8.RuneCountInString(text) > classificationText || !utf8.ValidString(text) || strings.TrimSpace(text) != text {
		return fmt.Errorf("journal: classification %s is not sanitized", what)
	}
	for _, r := range text {
		if unicode.IsControl(r) {
			return fmt.Errorf("journal: classification %s is not sanitized", what)
		}
	}
	return nil
}

func FinishRun(ctx context.Context, q db.Querier, runID int64, state string, now float64, errText *string) error {
	return progress.Write(ctx, q, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "UPDATE runs SET state = ?, error = ?, ended_at = ? WHERE id = ?", state, errText, now, runID); err != nil {
			return err
		}
		return progress.Finish(ctx, tx, runID, progress.Terminal{State: state, Time: now, Error: errText})
	})
}

func RequestStop(ctx context.Context, q db.Querier, runID int64) (bool, error) {
	return db.Changed(q.ExecContext(ctx, "UPDATE runs SET stop_requested = 1 WHERE id = ? AND state = ?", runID, Running))
}

func FinishUnlessQueued(ctx context.Context, d *db.DB, runID, threadID int64, state string, now float64) (bool, error) {
	finished := false
	err := d.WithTx(ctx, func(tx *sql.Tx) error {
		open, err := Queued(ctx, tx, threadID)
		if err != nil || len(open) > 0 {
			return err
		}
		finished = true
		return FinishRun(ctx, tx, runID, state, now, nil)
	})
	if err == nil {
		progress.Notify()
	}
	return finished && err == nil, err
}

func BumpSteps(ctx context.Context, q db.Querier, runID int64) error {
	_, err := q.ExecContext(ctx, "UPDATE runs SET steps = steps + 1 WHERE id = ?", runID)
	return err
}

func RenewLease(ctx context.Context, q db.Querier, runID int64, owner string, now float64) (bool, error) {
	lease, err := leaseSeconds(ctx, q)
	if err != nil {
		return false, err
	}
	return db.Changed(q.ExecContext(ctx, "UPDATE runs SET lease_until = ? WHERE id = ? AND owner = ? AND state = ?",
		now+lease, runID, owner, Running))
}

func MarkInterrupted(ctx context.Context, q db.Querier, now float64) (int64, error) {
	lease, err := leaseSeconds(ctx, q)
	if err != nil {
		return 0, err
	}
	if _, err := q.ExecContext(ctx,
		"UPDATE runs SET state = ?, error = 'crashed', ended_at = ? WHERE state = ? AND kind = ? AND started_at < ?",
		Failed, now, Accounting, classificationKind, now-lease); err != nil {
		return 0, err
	}
	res, err := q.ExecContext(ctx,
		"UPDATE runs SET state = ?, owner = NULL, lease_until = NULL WHERE state = ? AND (lease_until IS NULL OR lease_until < ?)",
		Interrupted, Running, now)
	if err != nil {
		return 0, err
	}
	progress.Notify()
	return res.RowsAffected()
}

func InterruptRuns(ctx context.Context, q db.Querier, runs []int64, now float64, dead string) error {
	if len(runs) == 0 {
		return nil
	}
	args := []any{Interrupted, Running, now, dead, dead, dead}
	for _, id := range runs {
		args = append(args, id)
	}
	_, err := q.ExecContext(ctx, "UPDATE runs SET state = ?, owner = NULL, lease_until = NULL WHERE state = ? "+
		"AND (lease_until IS NULL OR lease_until < ? OR (? <> '' AND substr(COALESCE(owner, ''), 1, length(?)) = ?)) AND id IN (?"+
		strings.Repeat(", ?", len(runs)-1)+")", args...)
	if err == nil {
		progress.Notify()
	}
	return err
}

func FailUnsettled(ctx context.Context, q db.Querier, runs []int64, now float64, text, self string) error {
	if len(runs) == 0 {
		return nil
	}
	args := []any{Failed, text, now, Running, Interrupted, self, self}
	for _, id := range runs {
		args = append(args, id)
	}
	return progress.Write(ctx, q, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, "UPDATE runs SET state = ?, error = ?, ended_at = ?, owner = NULL, lease_until = NULL WHERE state IN (?, ?) "+
			"AND (owner IS NULL OR substr(owner, 1, length(?)) = ?) AND id IN (?"+strings.Repeat(", ?", len(runs)-1)+") RETURNING id", args...)
		if err != nil {
			return err
		}
		ids, err := db.Collect(rows, func(r *sql.Rows) (int64, error) { var id int64; err := r.Scan(&id); return id, err })
		if err != nil {
			return err
		}
		for _, id := range ids {
			if err := progress.Finish(ctx, tx, id, progress.Terminal{State: Failed, Time: now, Error: &text}); err != nil {
				return err
			}
		}
		return nil
	})
}

func ClaimResume(ctx context.Context, q db.Querier, runID int64, owner string, now float64) (bool, error) {
	lease, err := leaseSeconds(ctx, q)
	if err != nil {
		return false, err
	}
	claimed, err := db.Changed(q.ExecContext(ctx, "UPDATE runs SET state = ?, owner = ?, lease_until = ? WHERE id = ? AND state IN (?, ?)",
		Running, owner, now+lease, runID, Interrupted, Parked))
	if claimed {
		progress.Notify()
	}
	return claimed, err
}

func InterruptedRuns(ctx context.Context, q db.Querier) ([]Run, error) {
	return runs(q.QueryContext(ctx, "SELECT "+runColumns+" FROM runs WHERE state = ? ORDER BY id", Interrupted))
}

func ActiveRun(ctx context.Context, q db.Querier, threadID int64) (*Run, error) {
	r, err := scanRun(q.QueryRowContext(ctx,
		"SELECT "+runColumns+" FROM runs WHERE thread_id = ? AND state IN (?, ?, ?) ORDER BY id DESC LIMIT 1", threadID, Running, Interrupted, Parked))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

func RunsOf(ctx context.Context, q db.Querier, threadID int64) ([]Run, error) {
	return runs(q.QueryContext(ctx, "SELECT "+runColumns+" FROM runs WHERE thread_id = ? ORDER BY id", threadID))
}

type Message struct {
	ID        int64             `json:"id"`
	RunID     *int64            `json:"run_id"`
	Seq       int64             `json:"seq"`
	Role      string            `json:"role"`
	Content   []json.RawMessage `json:"content"`
	CreatedAt float64           `json:"created_at"`
	SystemSig *string           `json:"system_sig"`
}

func dumps(content []json.RawMessage) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(content); err != nil {
		return "", fmt.Errorf("journal: content blocks: %w", err)
	}
	return strings.TrimSuffix(buf.String(), "\n"), nil
}

func messageText(content []json.RawMessage) (string, error) {
	texts, err := block.Texts(content)
	if err != nil {
		return "", fmt.Errorf("journal: %w", err)
	}
	return strings.TrimSpace(strings.Join(texts, "\n")), nil
}

func ReindexThread(ctx context.Context, q db.Querier, threadID int64) error {
	if _, err := q.ExecContext(ctx, "DELETE FROM thread_fts WHERE thread_id = ?", threadID); err != nil {
		return err
	}
	var title *string
	err := q.QueryRowContext(ctx, "SELECT title FROM threads WHERE id = ?", threadID).Scan(&title)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if title != nil && *title != "" {
		if _, err := q.ExecContext(ctx, "INSERT INTO thread_fts (thread_id, text) VALUES (?, ?)", threadID, *title); err != nil {
			return err
		}
	}
	rows, err := q.QueryContext(ctx, "SELECT content_json FROM thread_messages WHERE thread_id = ? AND superseded = 0 ORDER BY seq", threadID)
	if err != nil {
		return err
	}
	bodies, err := db.Collect(rows, func(r *sql.Rows) (string, error) {
		var raw string
		return raw, r.Scan(&raw)
	})
	if err != nil {
		return err
	}
	for _, raw := range bodies {
		var content []json.RawMessage
		if err := json.Unmarshal([]byte(raw), &content); err != nil {
			return fmt.Errorf("journal: thread %d content_json: %w", threadID, err)
		}
		text, err := messageText(content)
		if err != nil {
			return err
		}
		if text == "" {
			continue
		}
		if _, err := q.ExecContext(ctx, "INSERT INTO thread_fts (thread_id, text) VALUES (?, ?)", threadID, text); err != nil {
			return err
		}
	}
	return nil
}

func AppendMessageTx(ctx context.Context, q db.Querier, threadID int64, runID *int64, role string, content []json.RawMessage, now float64, systemSig *string) (int64, error) {
	body, err := dumps(content)
	if err != nil {
		return 0, err
	}
	var seq int64
	if err := q.QueryRowContext(ctx, "SELECT COALESCE(MAX(seq), 0) + 1 FROM thread_messages WHERE thread_id = ?", threadID).Scan(&seq); err != nil {
		return 0, err
	}
	if _, err := q.ExecContext(ctx,
		"INSERT INTO thread_messages (thread_id, run_id, seq, role, content_json, created_at, system_sig) VALUES (?, ?, ?, ?, ?, ?, ?)",
		threadID, runID, seq, role, body, now, systemSig); err != nil {
		return 0, err
	}
	if err := ReindexThread(ctx, q, threadID); err != nil {
		return 0, err
	}
	if _, err := q.ExecContext(ctx, "UPDATE threads SET updated_at = ? WHERE id = ?", now, threadID); err != nil {
		return 0, err
	}
	return seq, nil
}

func StoreResult(ctx context.Context, q db.Querier, runID, callID int64, tool, body string, tokens int64, now float64) (int64, error) {
	res, err := q.ExecContext(ctx,
		"INSERT INTO tool_results (run_id, tool_call_id, tool, tokens, body, created_at) VALUES (?, ?, ?, ?, ?, ?)",
		runID, callID, tool, tokens, body, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func AppendMessage(ctx context.Context, d *db.DB, threadID int64, runID *int64, role string, content []json.RawMessage, now float64, systemSig *string) (int64, error) {
	var seq int64
	err := d.WithTx(ctx, func(tx *sql.Tx) error {
		var err error
		seq, err = AppendMessageTx(ctx, tx, threadID, runID, role, content, now, systemSig)
		return err
	})
	return seq, err
}

func decodeContent(id int64, raw string) ([]json.RawMessage, error) {
	var content []json.RawMessage
	if err := json.Unmarshal([]byte(raw), &content); err != nil {
		return nil, fmt.Errorf("journal: message %d content_json: %w", id, err)
	}
	return content, nil
}

func MessagesOf(ctx context.Context, q db.Querier, threadID int64) ([]Message, error) {
	rows, err := q.QueryContext(ctx,
		"SELECT id, run_id, seq, role, content_json, created_at, system_sig FROM thread_messages WHERE thread_id = ? AND superseded = 0 ORDER BY seq",
		threadID)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (Message, error) {
		var m Message
		var raw string
		if err := r.Scan(&m.ID, &m.RunID, &m.Seq, &m.Role, &raw, &m.CreatedAt, &m.SystemSig); err != nil {
			return Message{}, err
		}
		var err error
		m.Content, err = decodeContent(m.ID, raw)
		return m, err
	})
}

type History struct {
	ThreadID int64
	Messages []Message
	Calls    []ToolCall
}

func LoadHistory(ctx context.Context, q db.Querier, threadID int64) (History, error) {
	messages, err := MessagesOf(ctx, q, threadID)
	if err != nil {
		return History{}, err
	}
	calls, err := ToolCallsForThread(ctx, q, threadID)
	if err != nil {
		return History{}, err
	}
	return History{ThreadID: threadID, Messages: messages, Calls: calls}, nil
}

func (h History) LastOperator() (*Message, error) {
	for i := len(h.Messages) - 1; i >= 0; i-- {
		if h.Messages[i].Role != User {
			continue
		}
		has, err := HasText(h.Messages[i].Content)
		if err != nil {
			return nil, err
		}
		if has {
			return &h.Messages[i], nil
		}
	}
	return nil, nil
}

func LastMessage(ctx context.Context, q db.Querier, threadID int64) (*Message, error) {
	var m Message
	var raw string
	err := q.QueryRowContext(ctx,
		"SELECT id, run_id, seq, role, content_json FROM thread_messages WHERE thread_id = ? AND superseded = 0 ORDER BY seq DESC LIMIT 1",
		threadID).Scan(&m.ID, &m.RunID, &m.Seq, &m.Role, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if m.Content, err = decodeContent(m.ID, raw); err != nil {
		return nil, err
	}
	return &m, nil
}

func HasText(content []json.RawMessage) (bool, error) {
	return block.HasText(content)
}

func Supersede(ctx context.Context, q db.Querier, threadID, fromSeq int64, keepMessage bool) error {
	first := fromSeq
	if keepMessage {
		first++
	}
	if _, err := q.ExecContext(ctx, "UPDATE thread_messages SET superseded = 1 WHERE thread_id = ? AND seq >= ?", threadID, first); err != nil {
		return err
	}
	var runID *int64
	err := q.QueryRowContext(ctx, "SELECT run_id FROM thread_messages WHERE thread_id = ? AND seq = ?", threadID, fromSeq).Scan(&runID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if runID != nil {
		if _, err := q.ExecContext(ctx, "UPDATE runs SET superseded = 1 WHERE thread_id = ? AND id >= ?", threadID, *runID); err != nil {
			return err
		}
	}
	return ReindexThread(ctx, q, threadID)
}

type QueuedMessage struct {
	ID         int64             `json:"id"`
	ThreadID   int64             `json:"thread_id"`
	Text       string            `json:"text"`
	CreatedAt  float64           `json:"created_at"`
	ConsumedAt *float64          `json:"consumed_at"`
	RunID      *int64            `json:"run_id"`
	FilesJSON  *string           `json:"files_json"`
	Files      []json.RawMessage `json:"files"`
}

func QueueMessage(ctx context.Context, d *db.DB, threadID int64, text string, now float64, files []json.RawMessage) (id int64, queued bool, err error) {
	body, err := dumps(append([]json.RawMessage{}, files...))
	if err != nil {
		return 0, false, err
	}
	err = d.WithTx(ctx, func(tx *sql.Tx) error {
		active, err := ActiveRun(ctx, tx, threadID)
		if err != nil || active == nil || active.State != Running {
			return err
		}
		res, err := tx.ExecContext(ctx, "INSERT INTO thread_queue (thread_id, text, created_at, files_json) VALUES (?, ?, ?, ?)", threadID, text, now, body)
		if err != nil {
			return err
		}
		queued = true
		id, err = res.LastInsertId()
		return err
	})
	return id, queued && err == nil, err
}

func Queued(ctx context.Context, q db.Querier, threadID int64) ([]QueuedMessage, error) {
	rows, err := q.QueryContext(ctx,
		"SELECT id, thread_id, text, created_at, consumed_at, run_id, files_json FROM thread_queue WHERE thread_id = ? AND consumed_at IS NULL ORDER BY id",
		threadID)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (QueuedMessage, error) {
		var m QueuedMessage
		if err := r.Scan(&m.ID, &m.ThreadID, &m.Text, &m.CreatedAt, &m.ConsumedAt, &m.RunID, &m.FilesJSON); err != nil {
			return QueuedMessage{}, err
		}
		m.Files = []json.RawMessage{}
		if m.FilesJSON != nil && *m.FilesJSON != "" {
			if err := json.Unmarshal([]byte(*m.FilesJSON), &m.Files); err != nil {
				return QueuedMessage{}, fmt.Errorf("journal: queued message %d files_json: %w", m.ID, err)
			}
		}
		return m, nil
	})
}

func ConsumeQueued(ctx context.Context, d *db.DB, threadID, runID int64, now float64, appendFn func(q db.Querier, m QueuedMessage) error) ([]QueuedMessage, error) {
	var taken []QueuedMessage
	err := d.WithTx(ctx, func(tx *sql.Tx) error {
		rows, err := Queued(ctx, tx, threadID)
		if err != nil {
			return err
		}
		for _, row := range rows {
			if err := appendFn(tx, row); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, "UPDATE thread_queue SET consumed_at = ?, run_id = ? WHERE id = ?", now, runID, row.ID); err != nil {
				return err
			}
			taken = append(taken, row)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return taken, nil
}

type ToolCall struct {
	ID          int64    `json:"id"`
	RunID       int64    `json:"run_id"`
	ToolUseID   string   `json:"tool_use_id"`
	Name        string   `json:"name"`
	ArgsJSON    string   `json:"args_json"`
	ArgsSHA256  string   `json:"args_sha256"`
	ReplayClass string   `json:"replay_class"`
	State       string   `json:"state"`
	Error       *string  `json:"error"`
	ResultID    *int64   `json:"result_id"`
	Result      *string  `json:"result"`
	StartedAt   float64  `json:"started_at"`
	FinishedAt  *float64 `json:"finished_at"`
}

const callColumns = "id, run_id, tool_use_id, name, args_json, args_sha256, replay_class, state, error, result_id, result, started_at, finished_at"

func scanCall(row interface{ Scan(...any) error }) (ToolCall, error) {
	var c ToolCall
	err := row.Scan(&c.ID, &c.RunID, &c.ToolUseID, &c.Name, &c.ArgsJSON, &c.ArgsSHA256, &c.ReplayClass, &c.State, &c.Error,
		&c.ResultID, &c.Result, &c.StartedAt, &c.FinishedAt)
	return c, err
}

func StartToolCall(ctx context.Context, q db.Querier, runID int64, toolUseID, name string, args json.RawMessage, replayClass string, now float64) (int64, error) {
	var buf bytes.Buffer
	if err := json.Compact(&buf, args); err != nil {
		return 0, fmt.Errorf("journal: tool args: %w", err)
	}
	body := buf.String()
	sum := sha256.Sum256([]byte(body))
	res, err := q.ExecContext(ctx,
		"INSERT INTO tool_calls (run_id, tool_use_id, name, args_json, args_sha256, replay_class, state, started_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
		runID, toolUseID, name, body, hex.EncodeToString(sum[:]), replayClass, Started, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func FinishToolCall(ctx context.Context, q db.Querier, callID int64, state string, now float64, errText *string, resultID *int64, result *string) error {
	_, err := q.ExecContext(ctx, "UPDATE tool_calls SET state = ?, error = ?, result_id = ?, result = ?, finished_at = ? WHERE id = ?",
		state, errText, resultID, result, now, callID)
	return err
}

func GetToolCall(ctx context.Context, q db.Querier, runID int64, toolUseID string) (*ToolCall, error) {
	c, err := scanCall(q.QueryRowContext(ctx, "SELECT "+callColumns+" FROM tool_calls WHERE run_id = ? AND tool_use_id = ?", runID, toolUseID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func calls(rows *sql.Rows, err error) ([]ToolCall, error) {
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (ToolCall, error) { return scanCall(r) })
}

type CallKey struct {
	RunID     int64
	ToolUseID string
}

func ToolCallsForThread(ctx context.Context, q db.Querier, threadID int64) ([]ToolCall, error) {
	return calls(q.QueryContext(ctx,
		"SELECT c.id, c.run_id, c.tool_use_id, c.name, c.args_json, c.args_sha256, c.replay_class, c.state, c.error, c.result_id, c.result, c.started_at, c.finished_at "+
			"FROM tool_calls c JOIN runs r ON r.id = c.run_id WHERE r.thread_id = ? AND r.superseded = 0 ORDER BY c.id", threadID))
}

// ParkRun releases the lease and marks the run parked until the operator
// answers the pending question.
func ParkRun(ctx context.Context, q db.Querier, runID int64, now float64) error {
	_, err := q.ExecContext(ctx, "UPDATE runs SET state = ?, owner = NULL, lease_until = NULL, ended_at = ? WHERE id = ?", Parked, now, runID)
	if err == nil {
		progress.Notify()
	}
	return err
}

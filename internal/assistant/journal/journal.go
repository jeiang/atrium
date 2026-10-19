// Package journal is agent/journal.py: the record of what the assistant did. Runs with their leases, the
// messages of a thread as the raw JSON content blocks the model exchanged, the queue of messages typed
// while a run is going, and the tool calls a run made.
package journal

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/jeiang/atrium/internal/assistant/store"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/py"
)

// Run states.
const (
	Running     = "running"
	Interrupted = "interrupted"
	Done        = "done"
	Failed      = "failed"
)

// Tool call states.
const (
	Started = "started"
	OK      = "done"
	Error   = "error"
	Unknown = "unknown"
)

// Replay classes of a tool: a safe call may be run again when a run resumes, a write may not.
const (
	Safe  = "safe"
	Write = "write"
)

// Message roles.
const (
	User      = "user"
	Assistant = "assistant"
)

// LeaseSeconds is how long a running run belongs to one process: the loop renews the lease each step, only
// a run whose lease lapsed can be marked interrupted, and resuming claims it atomically, so two processes
// never execute the same run.
const LeaseSeconds = 1800

// NewOwner is the identity of this process in run leases: host, pid and a random suffix.
func NewOwner() string {
	host, _ := os.Hostname()
	suffix := make([]byte, 4)
	_, _ = rand.Read(suffix)
	return fmt.Sprintf("%s:%d:%s", host, os.Getpid(), hex.EncodeToString(suffix))
}

var jsonCompact = py.JSONOpts{Compact: true, NoEnsureASCII: true}

// Run is a runs row.
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
	SkillsJSON    *string    `json:"skills_json"`
	Superseded    store.Flag `json:"superseded"`
	StopRequested store.Flag `json:"stop_requested"`
}

const runColumns = "id, thread_id, parent_run_id, kind, model, effort, state, error, steps, started_at, ended_at, limits_json, " +
	"owner, lease_until, skills_json, superseded, stop_requested"

func scanRun(row interface{ Scan(...any) error }) (Run, error) {
	var r Run
	err := row.Scan(&r.ID, &r.ThreadID, &r.ParentRunID, &r.Kind, &r.Model, &r.Effort, &r.State, &r.Error, &r.Steps, &r.StartedAt,
		&r.EndedAt, &r.LimitsJSON, &r.Owner, &r.LeaseUntil, &r.SkillsJSON, &r.Superseded, &r.StopRequested)
	return r, err
}

func runs(rows *sql.Rows, err error) ([]Run, error) {
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (Run, error) { return scanRun(r) })
}

func changed(res sql.Result, err error) (bool, error) {
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// StartRun is start_run: a running run holding its lease for LeaseSeconds. owner is nil for a run no process owns.
func StartRun(ctx context.Context, q db.Querier, threadID int64, kind, model, effort string, now float64, parentRunID *int64, owner *string) (int64, error) {
	res, err := q.ExecContext(ctx,
		"INSERT INTO runs (thread_id, parent_run_id, kind, model, effort, state, started_at, owner, lease_until) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
		threadID, parentRunID, kind, model, effort, Running, now, owner, now+LeaseSeconds)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// Limits are the per-run overrides loop.run takes, stored at start so a resumed run keeps them.
type Limits struct {
	MaxSteps     *int64
	MaxCostUSD   *float64
	SystemExtra  *string
	AllowedTools []string
}

func (l Limits) object() py.Dict {
	var o py.Dict
	if l.MaxSteps != nil {
		o = append(o, py.Pair{Key: "max_steps", Value: *l.MaxSteps})
	}
	if l.MaxCostUSD != nil {
		o = append(o, py.Pair{Key: "max_cost_usd", Value: *l.MaxCostUSD})
	}
	if l.SystemExtra != nil {
		o = append(o, py.Pair{Key: "system_extra", Value: *l.SystemExtra})
	}
	if l.AllowedTools != nil {
		o = append(o, py.Pair{Key: "allowed_tools", Value: l.AllowedTools})
	}
	return o
}

// SetLimits is set_limits.
func SetLimits(ctx context.Context, q db.Querier, runID int64, l Limits) error {
	_, err := q.ExecContext(ctx, "UPDATE runs SET limits_json = ? WHERE id = ?", py.JSONDumps(l.object()), runID)
	return err
}

// LimitsOf is limits_of: the overrides stored at start, none for an unknown run.
func LimitsOf(ctx context.Context, q db.Querier, runID int64) (Limits, error) {
	var raw *string
	err := q.QueryRowContext(ctx, "SELECT limits_json FROM runs WHERE id = ?", runID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) || err == nil && (raw == nil || *raw == "") {
		return Limits{}, nil
	}
	if err != nil {
		return Limits{}, err
	}
	var stored struct {
		MaxSteps     *int64   `json:"max_steps"`
		MaxCostUSD   *float64 `json:"max_cost_usd"`
		SystemExtra  *string  `json:"system_extra"`
		AllowedTools []string `json:"allowed_tools"`
	}
	if err := json.Unmarshal([]byte(*raw), &stored); err != nil {
		return Limits{}, fmt.Errorf("journal: run %d limits_json: %w", runID, err)
	}
	return Limits{stored.MaxSteps, stored.MaxCostUSD, stored.SystemExtra, stored.AllowedTools}, nil
}

// SetSkills is set_skills: the skills expanded into the run's operator message.
func SetSkills(ctx context.Context, q db.Querier, runID int64, names []string) error {
	_, err := q.ExecContext(ctx, "UPDATE runs SET skills_json = ? WHERE id = ?", py.JSONDumps(append([]string{}, names...)), runID)
	return err
}

// SkillsOf is skills_of.
func SkillsOf(r Run) ([]string, error) {
	if r.SkillsJSON == nil || *r.SkillsJSON == "" {
		return []string{}, nil
	}
	var names []string
	if err := json.Unmarshal([]byte(*r.SkillsJSON), &names); err != nil {
		return nil, fmt.Errorf("journal: run %d skills_json: %w", r.ID, err)
	}
	return names, nil
}

// GetRun is get_run: nil when there is no such run.
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

// FinishRun is finish_run.
func FinishRun(ctx context.Context, q db.Querier, runID int64, state string, now float64, errText *string) error {
	_, err := q.ExecContext(ctx, "UPDATE runs SET state = ?, error = ?, ended_at = ? WHERE id = ?", state, errText, now, runID)
	return err
}

// SetRunState is set_run_state.
func SetRunState(ctx context.Context, q db.Querier, runID int64, state string) error {
	_, err := q.ExecContext(ctx, "UPDATE runs SET state = ? WHERE id = ?", state, runID)
	return err
}

// RequestStop is request_stop: true when a running run was asked to stop.
func RequestStop(ctx context.Context, q db.Querier, runID int64) (bool, error) {
	return changed(q.ExecContext(ctx, "UPDATE runs SET stop_requested = 1 WHERE id = ? AND state = ?", runID, Running))
}

// FinishUnlessQueued is finish_unless_queued: it finishes the run unless the operator queued a message
// meanwhile. The queue insert and this check are both write transactions, so a message is either consumed
// by the run or starts the next one, never lost.
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
	return finished && err == nil, err
}

// BumpSteps is bump_steps.
func BumpSteps(ctx context.Context, q db.Querier, runID int64) error {
	_, err := q.ExecContext(ctx, "UPDATE runs SET steps = steps + 1 WHERE id = ?", runID)
	return err
}

// RenewLease is renew_lease: false when another process has taken the run over (it lapsed and was resumed
// elsewhere).
func RenewLease(ctx context.Context, q db.Querier, runID int64, owner string, now float64) (bool, error) {
	return changed(q.ExecContext(ctx, "UPDATE runs SET lease_until = ? WHERE id = ? AND owner = ? AND state = ?",
		now+LeaseSeconds, runID, owner, Running))
}

// MarkInterrupted is mark_interrupted: running runs whose lease lapsed become interrupted; it returns how many.
func MarkInterrupted(ctx context.Context, q db.Querier, now float64) (int64, error) {
	res, err := q.ExecContext(ctx,
		"UPDATE runs SET state = ?, owner = NULL, lease_until = NULL WHERE state = ? AND (lease_until IS NULL OR lease_until < ?)",
		Interrupted, Running, now)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ClaimResume is claim_resume: it atomically takes an interrupted run back to running under owner; false if
// it was not interrupted.
func ClaimResume(ctx context.Context, q db.Querier, runID int64, owner string, now float64) (bool, error) {
	return changed(q.ExecContext(ctx, "UPDATE runs SET state = ?, owner = ?, lease_until = ? WHERE id = ? AND state = ?",
		Running, owner, now+LeaseSeconds, runID, Interrupted))
}

// InterruptedRuns is interrupted_runs.
func InterruptedRuns(ctx context.Context, q db.Querier) ([]Run, error) {
	return runs(q.QueryContext(ctx, "SELECT "+runColumns+" FROM runs WHERE state = ? ORDER BY id", Interrupted))
}

// ActiveRun is active_run: the thread's latest running or interrupted run, nil when it has none.
func ActiveRun(ctx context.Context, q db.Querier, threadID int64) (*Run, error) {
	r, err := scanRun(q.QueryRowContext(ctx,
		"SELECT "+runColumns+" FROM runs WHERE thread_id = ? AND state IN (?, ?) ORDER BY id DESC LIMIT 1", threadID, Running, Interrupted))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// RunsOf is runs_of.
func RunsOf(ctx context.Context, q db.Querier, threadID int64) ([]Run, error) {
	return runs(q.QueryContext(ctx, "SELECT "+runColumns+" FROM runs WHERE thread_id = ? ORDER BY id", threadID))
}

// Message is a thread_messages row. Content holds each block as the JSON the model exchanged, so a block's
// fields the loop never reads (signatures, encrypted content) survive a round trip unchanged.
type Message struct {
	ID         int64             `json:"id"`
	RunID      *int64            `json:"run_id"`
	Seq        int64             `json:"seq"`
	Role       string            `json:"role"`
	Content    []json.RawMessage `json:"content"`
	CreatedAt  float64           `json:"created_at"`
	SystemSig  *string           `json:"system_sig"`
	Superseded bool              `json:"superseded"`
}

// Dumps is dumps: the journal's JSON text of a block list, compact with non-ASCII text kept as is.
func Dumps(content []json.RawMessage) (string, error) {
	items := make([]any, len(content))
	for i, b := range content {
		v, err := py.JSONLoads(b)
		if err != nil {
			return "", fmt.Errorf("journal: content block: %w", err)
		}
		items[i] = v
	}
	return py.JSONDumps(items, jsonCompact), nil
}

type textBlock struct {
	Type string  `json:"type"`
	Text *string `json:"text"`
}

// AppendMessage is append_message: it adds the message to the thread with the next seq, indexes its text
// blocks for search and touches the thread, all in one write transaction (parallel subagent runs append to
// the same journal). It returns the seq.
func AppendMessage(ctx context.Context, d *db.DB, threadID int64, runID *int64, role string, content []json.RawMessage, now float64, systemSig *string) (int64, error) {
	body, err := Dumps(content)
	if err != nil {
		return 0, err
	}
	var texts []string
	for _, raw := range content {
		var b textBlock
		if err := json.Unmarshal(raw, &b); err != nil {
			return 0, fmt.Errorf("journal: content block is not an object: %w", err)
		}
		if b.Type == "text" {
			t := ""
			if b.Text != nil {
				t = *b.Text
			}
			texts = append(texts, t)
		}
	}
	text := py.Strip(strings.Join(texts, "\n"))
	var seq int64
	err = d.WithTx(ctx, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(seq), 0) + 1 FROM thread_messages WHERE thread_id = ?", threadID).Scan(&seq); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO thread_messages (thread_id, run_id, seq, role, content_json, created_at, system_sig) VALUES (?, ?, ?, ?, ?, ?, ?)",
			threadID, runID, seq, role, body, now, systemSig); err != nil {
			return err
		}
		if text != "" {
			if _, err := tx.ExecContext(ctx, "INSERT INTO thread_fts (thread_id, text) VALUES (?, ?)", threadID, text); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, "UPDATE threads SET updated_at = ? WHERE id = ?", now, threadID)
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

// MessagesOf is messages_of: the thread's messages in order, without the superseded ones unless asked.
func MessagesOf(ctx context.Context, q db.Querier, threadID int64, includeSuperseded bool) ([]Message, error) {
	query := "SELECT id, run_id, seq, role, content_json, created_at, system_sig, superseded FROM thread_messages WHERE thread_id = ?"
	if !includeSuperseded {
		query += " AND superseded = 0"
	}
	rows, err := q.QueryContext(ctx, query+" ORDER BY seq", threadID)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (Message, error) {
		var m Message
		var raw string
		var superseded store.Flag
		if err := r.Scan(&m.ID, &m.RunID, &m.Seq, &m.Role, &raw, &m.CreatedAt, &m.SystemSig, &superseded); err != nil {
			return Message{}, err
		}
		m.Superseded = bool(superseded)
		var err error
		m.Content, err = decodeContent(m.ID, raw)
		return m, err
	})
}

// LastMessage is last_message: the newest live message, nil for an empty thread.
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

// HasText reports whether any block of content is a text block.
func HasText(content []json.RawMessage) bool {
	for _, raw := range content {
		var b textBlock
		if json.Unmarshal(raw, &b) == nil && b.Type == "text" {
			return true
		}
	}
	return false
}

// LastOperatorMessage is last_operator_message: the latest live operator (text) user message, the only one
// that can be edited or regenerated.
func LastOperatorMessage(ctx context.Context, q db.Querier, threadID int64) (*Message, error) {
	all, err := MessagesOf(ctx, q, threadID, false)
	if err != nil {
		return nil, err
	}
	for i := len(all) - 1; i >= 0; i-- {
		if all[i].Role == User && HasText(all[i].Content) {
			return &all[i], nil
		}
	}
	return nil, nil
}

// Supersede is supersede: it retires the tail of the conversation from fromSeq (the message itself stays when
// keepMessage). Messages and their runs are kept for audit but leave the active history, together with the
// context projections and staged ledger entries those runs made.
func Supersede(ctx context.Context, d *db.DB, threadID, fromSeq int64, keepMessage bool) error {
	return d.WithTx(ctx, func(tx *sql.Tx) error {
		first := fromSeq
		if keepMessage {
			first++
		}
		if _, err := tx.ExecContext(ctx, "UPDATE thread_messages SET superseded = 1 WHERE thread_id = ? AND seq >= ?", threadID, first); err != nil {
			return err
		}
		var runID *int64
		err := tx.QueryRowContext(ctx, "SELECT run_id FROM thread_messages WHERE thread_id = ? AND seq = ?", threadID, fromSeq).Scan(&runID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if runID != nil {
			if _, err := tx.ExecContext(ctx, "UPDATE runs SET superseded = 1 WHERE thread_id = ? AND id >= ?", threadID, *runID); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx,
				"DELETE FROM context_ledger WHERE state IN ('staged_drop', 'staged_fold') AND run_id IN (SELECT id FROM runs WHERE thread_id = ? AND superseded = 1)",
				threadID); err != nil {
				return err
			}
		}
		_, err = tx.ExecContext(ctx, "DELETE FROM context_projections WHERE thread_id = ? AND base_seq > ?", threadID, fromSeq)
		return err
	})
}

// QueuedMessage is a thread_queue row; Files are the attachment blocks stored with the text.
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

// QueueMessage is queue_message: it stores a message (text plus attachment blocks) for the running run to
// pick up at its next step. queued is false when no run is running (the caller starts a run instead).
func QueueMessage(ctx context.Context, d *db.DB, threadID int64, text string, now float64, files []json.RawMessage) (id int64, queued bool, err error) {
	body, err := Dumps(append([]json.RawMessage{}, files...))
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

// Queued is queued: the thread's messages no run has consumed yet, oldest first.
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

// TakeQueued is take_queued: it marks the queued messages consumed by the run and returns them.
func TakeQueued(ctx context.Context, q db.Querier, threadID, runID int64, now float64) ([]QueuedMessage, error) {
	rows, err := Queued(ctx, q, threadID)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		if _, err := q.ExecContext(ctx, "UPDATE thread_queue SET consumed_at = ?, run_id = ? WHERE id = ?", now, runID, r.ID); err != nil {
			return nil, err
		}
	}
	return rows, nil
}

// ToolCall is a tool_calls row.
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
	StartedAt   float64  `json:"started_at"`
	FinishedAt  *float64 `json:"finished_at"`
}

const callColumns = "id, run_id, tool_use_id, name, args_json, args_sha256, replay_class, state, error, result_id, started_at, finished_at"

func scanCall(row interface{ Scan(...any) error }) (ToolCall, error) {
	var c ToolCall
	err := row.Scan(&c.ID, &c.RunID, &c.ToolUseID, &c.Name, &c.ArgsJSON, &c.ArgsSHA256, &c.ReplayClass, &c.State, &c.Error,
		&c.ResultID, &c.StartedAt, &c.FinishedAt)
	return c, err
}

// StartToolCall is start_tool_call: it journals the call before the tool runs. args is the tool input as
// the model sent it; the row keeps it compact and hashes that text.
func StartToolCall(ctx context.Context, q db.Querier, runID int64, toolUseID, name string, args json.RawMessage, replayClass string, now float64) (int64, error) {
	body, err := py.JSONRedump(args, jsonCompact)
	if err != nil {
		return 0, err
	}
	sum := sha256.Sum256([]byte(body))
	res, err := q.ExecContext(ctx,
		"INSERT INTO tool_calls (run_id, tool_use_id, name, args_json, args_sha256, replay_class, state, started_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
		runID, toolUseID, name, body, hex.EncodeToString(sum[:]), replayClass, Started, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// FinishToolCall is finish_tool_call.
func FinishToolCall(ctx context.Context, q db.Querier, callID int64, state string, now float64, errText *string, resultID *int64) error {
	_, err := q.ExecContext(ctx, "UPDATE tool_calls SET state = ?, error = ?, result_id = ?, finished_at = ? WHERE id = ?",
		state, errText, resultID, now, callID)
	return err
}

// GetToolCall is tool_call: nil when the run made no such call.
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

// ToolCallsOf is tool_calls_of.
func ToolCallsOf(ctx context.Context, q db.Querier, runID int64) ([]ToolCall, error) {
	return calls(q.QueryContext(ctx, "SELECT "+callColumns+" FROM tool_calls WHERE run_id = ? ORDER BY id", runID))
}

// CallKey identifies a tool call within a thread.
type CallKey struct {
	RunID     int64
	ToolUseID string
}

// ToolCallsForThread is tool_calls_for_thread: every call the thread's runs made, by run and tool_use id.
func ToolCallsForThread(ctx context.Context, q db.Querier, threadID int64) (map[CallKey]ToolCall, error) {
	rows, err := q.QueryContext(ctx,
		"SELECT c.id, c.run_id, c.tool_use_id, c.name, c.args_json, c.args_sha256, c.replay_class, c.state, c.error, c.result_id, c.started_at, c.finished_at "+
			"FROM tool_calls c JOIN runs r ON r.id = c.run_id WHERE r.thread_id = ? ORDER BY c.id", threadID)
	list, err := calls(rows, err)
	if err != nil {
		return nil, err
	}
	out := make(map[CallKey]ToolCall, len(list))
	for _, c := range list {
		out[CallKey{c.RunID, c.ToolUseID}] = c
	}
	return out, nil
}

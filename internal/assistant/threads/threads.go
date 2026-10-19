// Package threads is agent/threads.py: conversation threads, their effort and context options, the URLs the
// operator allowed, listing and the purge of a deleted thread.
package threads

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/jeiang/atrium/internal/assistant/budget"
	"github.com/jeiang/atrium/internal/assistant/store"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/rx"
)

// Run and thread kinds. Chat, Cron, Nightly and Sub are the kinds a run (and its tool set) has; Subagent
// is the kind of a subagent's thread.
const (
	Chat     = "chat"
	Cron     = "cron"
	Nightly  = "nightly"
	Sub      = "sub"
	Subagent = "subagent"
)

const (
	Low    = "low"
	Medium = "medium"
	High   = "high"
	XHigh  = "xhigh"
	Max    = "max"
)

// Efforts are the thinking efforts a thread accepts.
var Efforts = [...]string{Low, Medium, High, XHigh, Max}

// EffortOf is the fixed effort of each kind of thread; chat threads take the operator's default.
var EffortOf = map[string]string{Chat: Medium, Subagent: Medium, Cron: High, Nightly: High, Sub: Medium}

// ContextLimits are the context sizes a thread accepts.
var ContextLimits = [...]int64{90_000, 200_000, 500_000, 1_000_000}

const (
	DefaultContextLimit = 90_000
	EffortKey           = "default_effort"
	ContextLimitKey     = "default_context_limit"
	TitleChars          = 80
	DefaultListLimit    = 50
)

var urlRe = rx.MustCompile(`https?://[^\s<>\"'()\[\]]+`, 0)

// OptionError is a refused effort or context limit.
type OptionError string

func (e OptionError) Error() string { return string(e) }

// CleanEffort is clean_effort.
func CleanEffort(value string) (string, error) {
	if !slices.Contains(Efforts[:], value) {
		return "", OptionError("effort must be one of " + strings.Join(Efforts[:], ", "))
	}
	return value, nil
}

// CleanContextLimit is clean_context_limit: value is what int() accepts, a number or a string.
func CleanContextLimit(value any) (int64, error) {
	limit, ok := int64(0), false
	switch v := value.(type) {
	case int:
		limit, ok = int64(v), true
	case int64:
		limit, ok = v, true
	case float64:
		if !math.IsNaN(v) && !math.IsInf(v, 0) && v > -1e18 && v < 1e18 {
			limit, ok = int64(v), true
		}
	case json.Number:
		limit, ok = py.ParseInt(string(v))
	case string:
		limit, ok = py.ParseInt(v)
	}
	if ok && slices.Contains(ContextLimits[:], limit) {
		return limit, nil
	}
	sizes := make([]string, len(ContextLimits))
	for i, n := range ContextLimits {
		sizes[i] = fmt.Sprintf("%dK", n/1000)
	}
	return 0, OptionError("context limit must be one of " + strings.Join(sizes, ", "))
}

// Options are the effort and context limit a new chat thread takes.
type Options struct {
	Effort       string `json:"effort"`
	ContextLimit int64  `json:"context_limit"`
}

// Defaults is defaults: the operator's defaults for new chat threads (Settings > Assistant).
func Defaults(ctx context.Context, q store.Querier) (Options, error) {
	effort, ok, err := budget.GetSetting(ctx, q, EffortKey)
	if err != nil {
		return Options{}, err
	}
	if !ok || !slices.Contains(Efforts[:], effort) {
		effort = EffortOf[Chat]
	}
	out := Options{Effort: effort, ContextLimit: DefaultContextLimit}
	limit, ok, err := budget.GetSetting(ctx, q, ContextLimitKey)
	if err != nil || !ok || limit == "" || strings.Trim(limit, "0123456789") != "" {
		return out, err
	}
	if n, ok := py.ParseInt(limit); ok && slices.Contains(ContextLimits[:], n) {
		out.ContextLimit = n
	}
	return out, nil
}

// SetDefaults is set_defaults.
func SetDefaults(ctx context.Context, q store.Querier, effort string, contextLimit any) error {
	e, err := CleanEffort(effort)
	if err != nil {
		return err
	}
	if err := budget.SetSetting(ctx, q, EffortKey, e); err != nil {
		return err
	}
	limit, err := CleanContextLimit(contextLimit)
	if err != nil {
		return err
	}
	return budget.SetSetting(ctx, q, ContextLimitKey, limit)
}

// TitleFrom is title_from: the first non-blank line, stripped, cut to TitleChars with an ellipsis.
func TitleFrom(text string) string {
	for _, line := range py.SplitLines(text) {
		line = py.Strip(line)
		if line == "" {
			continue
		}
		if utf8.RuneCountInString(line) <= TitleChars {
			return line
		}
		return py.Clip(line, TitleChars-1) + "…"
	}
	return ""
}

// NewThread are the arguments of Create. A nil Title, ParentID, JobID or ProjectID stores NULL.
type NewThread struct {
	Kind      string
	Title     *string
	ParentID  *int64
	JobID     *int64
	ProjectID *int64
}

// Create is create: a thread takes the options of its project, else the operator's defaults; a thread that
// is not a chat takes its kind's fixed effort.
func Create(ctx context.Context, q store.Querier, now float64, n NewThread) (int64, error) {
	chosen, err := Defaults(ctx, q)
	if err != nil {
		return 0, err
	}
	if n.ProjectID != nil && *n.ProjectID != 0 {
		err := q.QueryRowContext(ctx, "SELECT effort, context_limit FROM projects WHERE id = ?", *n.ProjectID).Scan(&chosen.Effort, &chosen.ContextLimit)
		if errors.Is(err, sql.ErrNoRows) {
			return 0, fmt.Errorf("threads: no project %d", *n.ProjectID)
		}
		if err != nil {
			return 0, err
		}
	}
	effort := chosen.Effort
	if n.Kind != Chat {
		var ok bool
		if effort, ok = EffortOf[n.Kind]; !ok {
			return 0, fmt.Errorf("threads: no effort for kind %s", py.Repr(n.Kind))
		}
	}
	res, err := q.ExecContext(ctx,
		"INSERT INTO threads (kind, title, job_id, parent_thread_id, effort, context_limit, project_id, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
		n.Kind, n.Title, n.JobID, n.ParentID, effort, chosen.ContextLimit, n.ProjectID, now, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// SetOptions is set_options: it changes a chat thread's effort and context limit; false when there is none.
func SetOptions(ctx context.Context, q store.Querier, threadID int64, effort string, contextLimit any) (bool, error) {
	e, err := CleanEffort(effort)
	if err != nil {
		return false, err
	}
	limit, err := CleanContextLimit(contextLimit)
	if err != nil {
		return false, err
	}
	return changed(q.ExecContext(ctx, "UPDATE threads SET effort = ?, context_limit = ? WHERE id = ? AND kind = ?", e, limit, threadID, Chat))
}

func changed(res sql.Result, err error) (bool, error) {
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// Thread is a threads row; AllowedURLs is its allowed_urls column decoded.
type Thread struct {
	ID                   int64      `json:"id"`
	Kind                 string     `json:"kind"`
	Title                *string    `json:"title"`
	JobID                *int64     `json:"job_id"`
	ParentThreadID       *int64     `json:"parent_thread_id"`
	Effort               string     `json:"effort"`
	AllowedURLs          []string   `json:"allowed_urls"`
	CreatedAt            float64    `json:"created_at"`
	UpdatedAt            float64    `json:"updated_at"`
	Archived             store.Flag `json:"archived"`
	Pinned               store.Flag `json:"pinned"`
	ContextLimit         *int64     `json:"context_limit"`
	BranchedFromThreadID *int64     `json:"branched_from_thread_id"`
	BranchedFromSeq      *int64     `json:"branched_from_seq"`
	Role                 *string    `json:"role"`
	ParentRunID          *int64     `json:"parent_run_id"`
	ProjectID            *int64     `json:"project_id"`
	CodeSandbox          store.Flag `json:"code_sandbox"`
	ContainerID          *string    `json:"container_id"`
	DeletedAt            *float64   `json:"deleted_at"`
}

// Columns is the column list Scan reads, in order.
const Columns = "id, kind, title, job_id, parent_thread_id, effort, allowed_urls, created_at, updated_at, archived, pinned, " +
	"context_limit, branched_from_thread_id, branched_from_seq, role, parent_run_id, project_id, code_sandbox, container_id, deleted_at"

// Scan reads one row selected with Columns.
func Scan(row interface{ Scan(...any) error }) (Thread, error) {
	var t Thread
	var urls string
	err := row.Scan(&t.ID, &t.Kind, &t.Title, &t.JobID, &t.ParentThreadID, &t.Effort, &urls, &t.CreatedAt, &t.UpdatedAt,
		&t.Archived, &t.Pinned, &t.ContextLimit, &t.BranchedFromThreadID, &t.BranchedFromSeq, &t.Role, &t.ParentRunID,
		&t.ProjectID, &t.CodeSandbox, &t.ContainerID, &t.DeletedAt)
	if err != nil {
		return Thread{}, err
	}
	if err := json.Unmarshal([]byte(urls), &t.AllowedURLs); err != nil {
		return Thread{}, fmt.Errorf("threads: thread %d allowed_urls: %w", t.ID, err)
	}
	return t, nil
}

// Get is get: the thread, or nil when there is none or it was purged.
func Get(ctx context.Context, q store.Querier, threadID int64) (*Thread, error) {
	t, err := Scan(q.QueryRowContext(ctx, "SELECT "+Columns+" FROM threads WHERE id = ? AND deleted_at IS NULL", threadID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// ContextLimitOf is context_limit_of: the thread's limit, or the default when it has none.
func ContextLimitOf(t Thread) int64 {
	if t.ContextLimit == nil || *t.ContextLimit == 0 {
		return DefaultContextLimit
	}
	return *t.ContextLimit
}

// SetTitle is set_title: it names a thread that has no title and indexes the title for search.
func SetTitle(ctx context.Context, q store.Querier, threadID int64, title string) error {
	changed, err := changed(q.ExecContext(ctx, "UPDATE threads SET title = ? WHERE id = ? AND title IS NULL", title, threadID))
	if err != nil || !changed {
		return err
	}
	_, err = q.ExecContext(ctx, "INSERT INTO thread_fts (thread_id, text) VALUES (?, ?)", threadID, title)
	return err
}

// RootKind is root_kind: the kind of the thread at the top of the parent chain.
func RootKind(ctx context.Context, q store.Querier, threadID int64) (string, error) {
	kind := ""
	seen := map[int64]bool{}
	for next := &threadID; next != nil && !seen[*next]; {
		seen[*next] = true
		var parent *int64
		var k string
		err := q.QueryRowContext(ctx, "SELECT kind, parent_thread_id FROM threads WHERE id = ?", *next).Scan(&k, &parent)
		if errors.Is(err, sql.ErrNoRows) {
			break
		}
		if err != nil {
			return "", err
		}
		kind, next = k, parent
	}
	return kind, nil
}

// AllowedURLs is allowed_urls: the URLs the operator gave the thread, which the agent may fetch.
func AllowedURLs(ctx context.Context, q store.Querier, threadID int64) (map[string]bool, error) {
	var raw string
	err := q.QueryRowContext(ctx, "SELECT allowed_urls FROM threads WHERE id = ?", threadID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return map[string]bool{}, nil
	}
	if err != nil {
		return nil, err
	}
	var urls []string
	if err := json.Unmarshal([]byte(raw), &urls); err != nil {
		return nil, fmt.Errorf("threads: thread %d allowed_urls: %w", threadID, err)
	}
	out := make(map[string]bool, len(urls))
	for _, u := range urls {
		out[u] = true
	}
	return out, nil
}

// AddAllowedURLs is add_allowed_urls: it merges urls into the thread's allowed set in one write transaction,
// so two runs adding at once lose neither.
func AddAllowedURLs(ctx context.Context, d *db.DB, threadID int64, urls []string) error {
	var add []string
	for _, u := range urls {
		if u != "" {
			add = append(add, u)
		}
	}
	if len(add) == 0 {
		return nil
	}
	return d.WithTx(ctx, func(tx *sql.Tx) error {
		known, err := AllowedURLs(ctx, tx, threadID)
		if err != nil {
			return err
		}
		merged := maps.Clone(known)
		for _, u := range add {
			merged[u] = true
		}
		if len(merged) == len(known) {
			return nil
		}
		list := make([]string, 0, len(merged))
		for u := range merged {
			list = append(list, u)
		}
		slices.Sort(list)
		text, err := py.JSONDumps(list, py.JSONOpts{EnsureASCII: true})
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "UPDATE threads SET allowed_urls = ? WHERE id = ?", text, threadID)
		return err
	})
}

// URLsIn is urls_in: every http(s) URL in text without the punctuation that trails it.
func URLsIn(text string) []string {
	matches, _ := urlRe.FindIter(text)
	out := make([]string, len(matches))
	for i, m := range matches {
		out[i] = strings.TrimRight(m.Text(), ".,;:!?")
	}
	return out
}

// Listed is one row of the thread list.
type Listed struct {
	ID          int64      `json:"id"`
	Kind        string     `json:"kind"`
	Title       *string    `json:"title"`
	Pinned      store.Flag `json:"pinned"`
	Archived    store.Flag `json:"archived"`
	CreatedAt   float64    `json:"created_at"`
	UpdatedAt   float64    `json:"updated_at"`
	ProjectID   *int64     `json:"project_id"`
	ProjectName *string    `json:"project_name"`
	RunState    *string    `json:"run_state"`
	Cost        float64    `json:"cost"`
}

// ListOpts are the arguments of ListThreads. Match is the FTS5 query for the search text (search.FTSQuery),
// empty for none; a zero ProjectID lists every project.
type ListOpts struct {
	Match     string
	Limit     int
	Archived  bool
	ProjectID int64
}

const listSQL = "SELECT t.id, t.kind, t.title, t.pinned, t.archived, t.created_at, t.updated_at, t.project_id, " +
	"(SELECT name FROM projects p WHERE p.id = t.project_id) AS project_name, " +
	"(SELECT state FROM runs r WHERE r.thread_id = t.id ORDER BY r.id DESC LIMIT 1) AS run_state, " +
	"(SELECT COALESCE(SUM(cost_usd), 0) FROM usage u WHERE u.thread_id = t.id) AS cost " +
	"FROM threads t WHERE t.kind = ? AND t.archived = ? AND t.deleted_at IS NULL"

// ListThreads is list_threads: chat threads, pinned first, newest first.
func ListThreads(ctx context.Context, q store.Querier, o ListOpts) ([]Listed, error) {
	query := listSQL
	args := []any{Chat, store.Bit(o.Archived)}
	if o.ProjectID != 0 {
		query += " AND t.project_id = ?"
		args = append(args, o.ProjectID)
	}
	if o.Match != "" {
		query += " AND t.id IN (SELECT thread_id FROM thread_fts WHERE thread_fts MATCH ?)"
		args = append(args, o.Match)
	}
	query += " ORDER BY t.pinned DESC, t.updated_at DESC, t.id DESC LIMIT ?"
	args = append(args, o.Limit)
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (Listed, error) {
		var l Listed
		return l, r.Scan(&l.ID, &l.Kind, &l.Title, &l.Pinned, &l.Archived, &l.CreatedAt, &l.UpdatedAt, &l.ProjectID,
			&l.ProjectName, &l.RunState, &l.Cost)
	})
}

// SetFlag is set_flag: column is "pinned", "archived" or "code_sandbox" of a live chat thread.
func SetFlag(ctx context.Context, q store.Querier, threadID int64, column string, value bool) (bool, error) {
	switch column {
	case "pinned", "archived", "code_sandbox":
	default:
		return false, fmt.Errorf("threads: %s", py.Repr(column))
	}
	return changed(q.ExecContext(ctx,
		"UPDATE threads SET "+column+" = ? WHERE id = ? AND kind = ? AND deleted_at IS NULL", store.Bit(value), threadID, Chat))
}

// SetContainer is set_container.
func SetContainer(ctx context.Context, q store.Querier, threadID int64, containerID *string) error {
	_, err := q.ExecContext(ctx, "UPDATE threads SET container_id = ? WHERE id = ?", containerID, threadID)
	return err
}

// Purge is purge: it deletes a thread's conversation, queue, plan, projections and tool records. The thread
// row stays as a tombstone (deleted_at set, hidden everywhere) because usage, agent_writes and runs
// reference it for accounting. Run it in one transaction.
func Purge(ctx context.Context, q store.Querier, threadID int64, now float64) error {
	const runs = "SELECT id FROM runs WHERE thread_id = ?"
	for _, stmt := range []string{
		"DELETE FROM run_events WHERE run_id IN (" + runs + ")",
		"DELETE FROM context_ledger WHERE run_id IN (" + runs + ")",
		"DELETE FROM tool_results WHERE run_id IN (" + runs + ")",
		"DELETE FROM tool_calls WHERE run_id IN (" + runs + ")",
		"DELETE FROM context_projections WHERE thread_id = ?",
		"DELETE FROM thread_queue WHERE thread_id = ?",
		"DELETE FROM thread_plans WHERE thread_id = ?",
		"DELETE FROM thread_messages WHERE thread_id = ?",
		"DELETE FROM thread_fts WHERE thread_id = ?",
	} {
		if _, err := q.ExecContext(ctx, stmt, threadID); err != nil {
			return err
		}
	}
	_, err := q.ExecContext(ctx,
		"UPDATE threads SET deleted_at = ?, archived = 1, pinned = 0, title = NULL, container_id = NULL WHERE id = ?", now, threadID)
	return err
}

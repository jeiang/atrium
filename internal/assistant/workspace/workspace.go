// Package workspace is agent/workspace.py without its view assembly: a thread's plan, its files, its
// subagent threads and the sources its tools and searches read.
package workspace

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jeiang/atrium/internal/assistant/journal"
	"github.com/jeiang/atrium/internal/assistant/store"
	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/py"
)

const (
	PlanItems     = 20
	PlanItemChars = 200

	WebFetch       = "web_fetch"
	MailRead       = "mail_read"
	FetchKnowledge = "fetch_knowledge"

	toolResult   = "tool_result"
	webSearchRes = "web_search_tool_result"

	KindWeb    = "web"
	KindMail   = "mail"
	KindNotion = "notion"
)

// PlanError is a refused plan.
type PlanError string

func (e PlanError) Error() string { return string(e) }

// PlanItem is one step of a thread's plan.
type PlanItem struct {
	Text string `json:"text"`
	Done bool   `json:"done"`
}

// CleanPlan is clean_plan: items is the decoded JSON list the model sent, each step an object with a text.
func CleanPlan(items any) ([]PlanItem, error) {
	list, ok := items.([]any)
	if !ok || len(list) > PlanItems {
		return nil, PlanError(fmt.Sprintf("items must be a list of at most %d steps", PlanItems))
	}
	out := make([]PlanItem, 0, len(list))
	for _, item := range list {
		step, _ := item.(map[string]any)
		text, isText := step["text"].(string)
		if step == nil || !isText || py.Strip(text) == "" {
			return nil, PlanError("each step needs a non-empty text")
		}
		out = append(out, PlanItem{Text: py.Clip(py.Strip(text), PlanItemChars), Done: py.Truthy(step["done"])})
	}
	return out, nil
}

// SetPlan is set_plan: it replaces the thread's plan and returns the cleaned items.
func SetPlan(ctx context.Context, q store.Querier, threadID int64, runID *int64, items any, now float64) ([]PlanItem, error) {
	clean, err := CleanPlan(items)
	if err != nil {
		return nil, err
	}
	pairs := make([]any, len(clean))
	for i, it := range clean {
		pairs[i] = py.Dict{{Key: "text", Value: it.Text}, {Key: "done", Value: it.Done}}
	}
	text, err := py.Dumps(pairs, py.JSONOpts{})
	if err != nil {
		return nil, err
	}
	_, err = q.ExecContext(ctx,
		"INSERT INTO thread_plans (thread_id, run_id, items_json, updated_at) VALUES (?, ?, ?, ?) "+
			"ON CONFLICT(thread_id) DO UPDATE SET run_id = excluded.run_id, items_json = excluded.items_json, updated_at = excluded.updated_at",
		threadID, runID, text, now)
	return clean, err
}

// Plan is a thread's plan.
type Plan struct {
	Items     []PlanItem `json:"items"`
	UpdatedAt float64    `json:"updated_at"`
	RunID     *int64     `json:"run_id"`
}

// PlanOf is plan_of: nil when the thread has no plan.
func PlanOf(ctx context.Context, q store.Querier, threadID int64) (*Plan, error) {
	var p Plan
	var raw string
	err := q.QueryRowContext(ctx, "SELECT items_json, updated_at, run_id FROM thread_plans WHERE thread_id = ?", threadID).Scan(&raw, &p.UpdatedAt, &p.RunID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(raw), &p.Items); err != nil {
		return nil, fmt.Errorf("workspace: thread %d plan: %w", threadID, err)
	}
	return &p, nil
}

// File is a thread_files row. The anthropic_file_id column is plain data; Phase A has no code execution
// that reads it.
type File struct {
	ID              int64   `json:"id"`
	ThreadID        int64   `json:"thread_id"`
	RunID           *int64  `json:"run_id"`
	Name            string  `json:"name"`
	MediaType       string  `json:"media_type"`
	Bytes           int64   `json:"bytes"`
	Path            string  `json:"path"`
	Origin          string  `json:"origin"`
	CreatedAt       float64 `json:"created_at"`
	AnthropicFileID *string `json:"anthropic_file_id"`
	Text            *string `json:"text"`
	Tokens          int64   `json:"tokens"`
	ProjectID       *int64  `json:"project_id"`
}

const fileColumns = "id, thread_id, run_id, name, media_type, bytes, path, origin, created_at, anthropic_file_id, text, tokens, project_id"

func scanFile(r interface{ Scan(...any) error }) (File, error) {
	var f File
	err := r.Scan(&f.ID, &f.ThreadID, &f.RunID, &f.Name, &f.MediaType, &f.Bytes, &f.Path, &f.Origin, &f.CreatedAt,
		&f.AnthropicFileID, &f.Text, &f.Tokens, &f.ProjectID)
	return f, err
}

// FilesOf is files_of.
func FilesOf(ctx context.Context, q store.Querier, threadID int64) ([]File, error) {
	rows, err := q.QueryContext(ctx, "SELECT "+fileColumns+" FROM thread_files WHERE thread_id = ? ORDER BY id", threadID)
	if err != nil {
		return nil, err
	}
	return store.Collect(rows, func(r *sql.Rows) (File, error) { return scanFile(r) })
}

// GetFile is get_file: nil when the thread has no such file.
func GetFile(ctx context.Context, q store.Querier, threadID, fileID int64) (*File, error) {
	f, err := scanFile(q.QueryRowContext(ctx, "SELECT "+fileColumns+" FROM thread_files WHERE id = ? AND thread_id = ?", fileID, threadID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &f, nil
}

// ChildThreads is child_threads: the thread's live subagent threads.
func ChildThreads(ctx context.Context, q store.Querier, threadID int64) ([]threads.Thread, error) {
	rows, err := q.QueryContext(ctx,
		"SELECT "+threads.Columns+" FROM threads WHERE parent_thread_id = ? AND deleted_at IS NULL ORDER BY id", threadID)
	if err != nil {
		return nil, err
	}
	return store.Collect(rows, func(r *sql.Rows) (threads.Thread, error) { return threads.Scan(r) })
}

// Source is something the thread read. Via names the subagent role that read it, nil for the thread itself.
type Source struct {
	Kind      string  `json:"kind"`
	Key       string  `json:"key"`
	Title     string  `json:"title"`
	URL       string  `json:"url"`
	Via       *string `json:"via"`
	MessageID *int64  `json:"message_id,omitempty"`
	PageID    *string `json:"page_id,omitempty"`
}

func parse(body string) map[string]any {
	var found map[string]any
	if err := json.Unmarshal([]byte(body), &found); err != nil {
		return nil
	}
	return found
}

func str(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}

func first(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

type resultBlock struct {
	Type      string          `json:"type"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
}

// resultBodies is result_bodies: tool_use id to the full result text, the stored body for stubbed results,
// else the journaled tool_result.
func resultBodies(ctx context.Context, q store.Querier, threadIDs []int64) (map[string]string, error) {
	bodies := map[string]string{}
	for _, id := range threadIDs {
		msgs, err := journal.MessagesOf(ctx, q, id, false)
		if err != nil {
			return nil, err
		}
		for _, m := range msgs {
			if m.Role != journal.User {
				continue
			}
			for _, raw := range m.Content {
				var b resultBlock
				var text string
				if json.Unmarshal(raw, &b) == nil && b.Type == toolResult && json.Unmarshal(b.Content, &text) == nil {
					bodies[b.ToolUseID] = text
				}
			}
		}
	}
	marks, args := inList(threadIDs)
	rows, err := q.QueryContext(ctx,
		"SELECT c.tool_use_id, t.body FROM tool_calls c JOIN runs r ON r.id = c.run_id JOIN tool_results t ON t.result_id = c.result_id "+
			"WHERE r.thread_id IN ("+marks+")", args...)
	if err != nil {
		return nil, err
	}
	stored, err := store.Collect(rows, func(r *sql.Rows) ([2]string, error) {
		var kv [2]string
		return kv, r.Scan(&kv[0], &kv[1])
	})
	for _, kv := range stored {
		bodies[kv[0]] = kv[1]
	}
	return bodies, err
}

func inList(ids []int64) (string, []any) {
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	return strings.TrimSuffix(strings.Repeat("?,", len(ids)), ","), args
}

type threadCall struct {
	journal.ToolCall
	ThreadID int64
	Via      *string
}

func callsOfThreads(ctx context.Context, q store.Querier, threadIDs []int64) ([]threadCall, error) {
	marks, args := inList(threadIDs)
	rows, err := q.QueryContext(ctx,
		"SELECT c.id, c.run_id, c.tool_use_id, c.name, c.args_json, c.args_sha256, c.replay_class, c.state, c.error, c.result_id, "+
			"c.started_at, c.finished_at, r.thread_id, t.role AS via "+
			"FROM tool_calls c JOIN runs r ON r.id = c.run_id JOIN threads t ON t.id = r.thread_id "+
			"WHERE r.thread_id IN ("+marks+") ORDER BY c.id", args...)
	if err != nil {
		return nil, err
	}
	return store.Collect(rows, func(r *sql.Rows) (threadCall, error) {
		var c threadCall
		err := r.Scan(&c.ID, &c.RunID, &c.ToolUseID, &c.Name, &c.ArgsJSON, &c.ArgsSHA256, &c.ReplayClass, &c.State, &c.Error,
			&c.ResultID, &c.StartedAt, &c.FinishedAt, &c.ThreadID, &c.Via)
		return c, err
	})
}

func searchSources(ctx context.Context, q store.Querier, threadID int64, children []threads.Thread) ([]Source, error) {
	type owner struct {
		id   int64
		role *string
	}
	owners := []owner{{threadID, nil}}
	for _, c := range children {
		owners = append(owners, owner{c.ID, c.Role})
	}
	var found []Source
	for _, t := range owners {
		msgs, err := journal.MessagesOf(ctx, q, t.id, false)
		if err != nil {
			return nil, err
		}
		for _, m := range msgs {
			for _, raw := range m.Content {
				var b resultBlock
				var results []json.RawMessage
				if json.Unmarshal(raw, &b) != nil || b.Type != webSearchRes || json.Unmarshal(b.Content, &results) != nil {
					continue
				}
				for _, r := range results {
					var hit map[string]any
					if json.Unmarshal(r, &hit) != nil || str(hit, "url") == "" {
						continue
					}
					url := str(hit, "url")
					found = append(found, Source{Kind: KindWeb, Key: url, Title: first(str(hit, "title"), url), URL: url, Via: t.role})
				}
			}
		}
	}
	return found, nil
}

func isInt(v any) (int64, bool) {
	n, ok := v.(json.Number)
	if !ok || strings.ContainsAny(string(n), ".eE") {
		return 0, false
	}
	id, err := n.Int64()
	return id, err == nil
}

func sourceOf(c journal.ToolCall, result map[string]any) (*Source, error) {
	dec := json.NewDecoder(strings.NewReader(c.ArgsJSON))
	dec.UseNumber()
	var args map[string]any
	if err := dec.Decode(&args); err != nil {
		return nil, fmt.Errorf("workspace: tool call %d args_json: %w", c.ID, err)
	}
	switch c.Name {
	case WebFetch:
		if argURL := str(args, "url"); argURL != "" {
			url := first(str(result, "url"), argURL)
			return &Source{Kind: KindWeb, Key: url, Title: first(str(result, "title"), url), URL: url}, nil
		}
	case MailRead:
		if id, ok := isInt(args["message_id"]); ok {
			headers, _ := result["headers"].(map[string]any)
			return &Source{
				Kind: KindMail, Key: fmt.Sprintf("m%d", id), Title: first(str(headers, "Subject"), fmt.Sprintf("Message %d", id)),
				URL: fmt.Sprintf("/messages/%d", id), MessageID: &id,
			}, nil
		}
	case FetchKnowledge:
		if argPage := str(args, "page_id"); argPage != "" {
			page := first(str(result, "page_id"), argPage)
			return &Source{Kind: KindNotion, Key: "n" + page, Title: first(str(result, "title"), page), URL: "/notion/" + page, PageID: &page}, nil
		}
	}
	return nil, nil
}

// SourcesOf is sources_of: what the thread (and its subagents) read, fetched pages, search hits, mail
// messages and Notion pages, each once.
func SourcesOf(ctx context.Context, q store.Querier, threadID int64) ([]Source, error) {
	children, err := ChildThreads(ctx, q, threadID)
	if err != nil {
		return nil, err
	}
	return SourcesWith(ctx, q, threadID, children)
}

// SourcesWith is sources_of with the subagent threads already loaded.
func SourcesWith(ctx context.Context, q store.Querier, threadID int64, children []threads.Thread) ([]Source, error) {
	ids := []int64{threadID}
	for _, c := range children {
		ids = append(ids, c.ID)
	}
	bodies, err := resultBodies(ctx, q, ids)
	if err != nil {
		return nil, err
	}
	calls, err := callsOfThreads(ctx, q, ids)
	if err != nil {
		return nil, err
	}
	var found []Source
	for _, c := range calls {
		if c.State != journal.OK {
			continue
		}
		s, err := sourceOf(c.ToolCall, parse(bodies[c.ToolUseID]))
		if err != nil {
			return nil, err
		}
		if s == nil {
			continue
		}
		if c.ThreadID != threadID {
			s.Via = c.Via
		}
		found = append(found, *s)
	}
	hits, err := searchSources(ctx, q, threadID, children)
	if err != nil {
		return nil, err
	}
	found = append(found, hits...)
	out := []Source{}
	seen := map[string]bool{}
	for _, s := range found {
		if !seen[s.Key] {
			seen[s.Key] = true
			out = append(out, s)
		}
	}
	return out, nil
}

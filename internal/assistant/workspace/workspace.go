package workspace

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/jsonx"
	"github.com/jeiang/atrium/internal/textx"

	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/assistant/block"
	"github.com/jeiang/atrium/internal/assistant/journal"
	"github.com/jeiang/atrium/internal/assistant/threads"
)

const (
	WebFetch = "web_fetch"
	MailRead = "mail_read"
	KBGet    = "kb_get"

	KindWeb       = "web"
	KindMail      = "mail"
	KindKnowledge = "knowledge"
)

type PlanError string

func (e PlanError) Error() string { return string(e) }

type PlanItem struct {
	Text string `json:"text"`
	Done bool   `json:"done"`
}

func CleanPlan(ctx context.Context, q db.Querier, items any) ([]PlanItem, error) {
	r := assistant.Read(ctx, q)
	maxItems, maxChars := r.Int(assistant.KeyPlanItems), r.Int(assistant.KeyPlanItemChars)
	if err := r.Err(); err != nil {
		return nil, err
	}
	list, ok := items.([]any)
	if !ok || len(list) > maxItems {
		return nil, PlanError(fmt.Sprintf("items must be a list of at most %d steps", maxItems))
	}
	out := make([]PlanItem, 0, len(list))
	for _, item := range list {
		var rawText, done any
		switch step := item.(type) {
		case map[string]any:
			rawText, done = step["text"], step["done"]
		}
		text, isText := rawText.(string)
		if !isText || strings.TrimSpace(text) == "" {
			return nil, PlanError("each step needs a non-empty text")
		}
		out = append(out, PlanItem{Text: textx.Clip(strings.TrimSpace(text), maxChars), Done: jsonx.Truthy(done)})
	}
	return out, nil
}

func SetPlan(ctx context.Context, q db.Querier, threadID int64, runID *int64, items any, now float64) ([]PlanItem, error) {
	clean, err := CleanPlan(ctx, q, items)
	if err != nil {
		return nil, err
	}
	text, err := json.Marshal(clean)
	if err != nil {
		return nil, err
	}
	_, err = q.ExecContext(ctx,
		"INSERT INTO thread_plans (thread_id, run_id, items_json, updated_at) VALUES (?, ?, ?, ?) "+
			"ON CONFLICT(thread_id) DO UPDATE SET run_id = excluded.run_id, items_json = excluded.items_json, updated_at = excluded.updated_at",
		threadID, runID, text, now)
	return clean, err
}

type Plan struct {
	Items     []PlanItem `json:"items"`
	UpdatedAt float64    `json:"updated_at"`
	RunID     *int64     `json:"run_id"`
}

func PlanOf(ctx context.Context, q db.Querier, threadID int64) (*Plan, error) {
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

func ChildThreads(ctx context.Context, q db.Querier, threadID int64) ([]threads.Thread, error) {
	rows, err := q.QueryContext(ctx,
		"SELECT "+threads.Columns+" FROM threads WHERE parent_thread_id = ? AND deleted_at IS NULL ORDER BY id", threadID)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (threads.Thread, error) { return threads.Scan(r) })
}

type Source struct {
	Kind      string  `json:"kind"`
	Key       string  `json:"key"`
	Title     string  `json:"title"`
	URL       string  `json:"url"`
	Via       *string `json:"via"`
	MessageID *int64  `json:"message_id,omitempty"`
	RecordID  *int64  `json:"record_id,omitempty"`
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

func resultBodies(histories []journal.History) (map[string]string, error) {
	bodies := map[string]string{}
	for _, h := range histories {
		for _, m := range h.Messages {
			if m.Role != journal.User {
				continue
			}
			blocks, err := block.DecodeAll(m.Content)
			if err != nil {
				return nil, err
			}
			for _, b := range blocks {
				if b.Kind != block.ToolResult {
					continue
				}
				if text, ok := b.Get("content").(string); ok {
					bodies[b.String("tool_use_id")] = text
				}
			}
		}
	}
	return bodies, nil
}

func inList(ids []int64) (string, []any) {
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	return strings.TrimSuffix(strings.Repeat("?,", len(ids)), ","), args
}

func storedBodies(ctx context.Context, q db.Querier, threadIDs []int64) (map[string]string, error) {
	marks, args := inList(threadIDs)
	rows, err := q.QueryContext(ctx,
		"SELECT c.tool_use_id, t.body FROM tool_calls c JOIN runs r ON r.id = c.run_id JOIN tool_results t ON t.result_id = c.result_id "+
			"WHERE r.thread_id IN ("+marks+") AND r.superseded = 0", args...)
	if err != nil {
		return nil, err
	}
	stored, err := db.Collect(rows, func(r *sql.Rows) ([2]string, error) {
		var kv [2]string
		return kv, r.Scan(&kv[0], &kv[1])
	})
	if err != nil {
		return nil, err
	}
	bodies := make(map[string]string, len(stored))
	for _, kv := range stored {
		bodies[kv[0]] = kv[1]
	}
	return bodies, nil
}

func searchSources(histories []journal.History, roles map[int64]*string) ([]Source, error) {
	var found []Source
	for _, h := range histories {
		for _, m := range h.Messages {
			blocks, err := block.DecodeAll(m.Content)
			if err != nil {
				return nil, err
			}
			for _, b := range blocks {
				if b.Kind != block.WebSearchResult {
					continue
				}
				results, ok := b.Get("content").([]any)
				if !ok {
					continue
				}
				for _, r := range results {
					hit, ok := r.(map[string]any)
					if !ok {
						continue
					}
					url := hit["url"]
					link, _ := url.(string)
					if link == "" {
						continue
					}
					title := hit["title"]
					label, _ := title.(string)
					found = append(found, Source{Kind: KindWeb, Key: link, Title: first(label, link), URL: link, Via: roles[h.ThreadID]})
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
	case KBGet:
		if ids, ok := args["ids"].([]any); ok && len(ids) > 0 {
			if id, ok := isInt(ids[0]); ok {
				title := fmt.Sprintf("Record %d", id)
				if results, ok := result["results"].([]any); ok && len(results) > 0 {
					if item, ok := results[0].(map[string]any); ok {
						title = first(str(item, "title"), title)
					}
				}
				return &Source{Kind: KindKnowledge, Key: fmt.Sprintf("k%d", id), Title: title, URL: fmt.Sprintf("/library/knowledge/%d", id), RecordID: &id}, nil
			}
		}
	}
	return nil, nil
}

type ownedCall struct {
	journal.ToolCall
	ThreadID int64
}

func SourcesWith(ctx context.Context, q db.Querier, threadID int64, children []threads.Thread) ([]Source, error) {
	ids := []int64{threadID}
	roles := map[int64]*string{threadID: nil}
	for _, c := range children {
		ids = append(ids, c.ID)
		roles[c.ID] = c.Role
	}
	histories := make([]journal.History, 0, len(ids))
	var calls []ownedCall
	for _, id := range ids {
		h, err := journal.LoadHistory(ctx, q, id)
		if err != nil {
			return nil, err
		}
		histories = append(histories, h)
		for _, c := range h.Calls {
			calls = append(calls, ownedCall{c, id})
		}
	}
	slices.SortFunc(calls, func(a, b ownedCall) int { return cmp.Compare(a.ID, b.ID) })
	bodies, err := resultBodies(histories)
	if err != nil {
		return nil, err
	}
	stored, err := storedBodies(ctx, q, ids)
	if err != nil {
		return nil, err
	}
	maps.Copy(bodies, stored)
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
			s.Via = roles[c.ThreadID]
		}
		found = append(found, *s)
	}
	searched, err := searchSources(histories, roles)
	if err != nil {
		return nil, err
	}
	found = append(found, searched...)
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

package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"strings"

	"github.com/jeiang/atrium/internal/assistant/block"
	actx "github.com/jeiang/atrium/internal/assistant/context"
	"github.com/jeiang/atrium/internal/assistant/journal"
	"github.com/jeiang/atrium/internal/assistant/progress"
	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/assistant/usage"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/py"
)

const draftCreate = "draft_email_create"

const (
	ItemUser       = "user"
	ItemActivity   = "activity"
	ItemAssistant  = "assistant"
	ItemDraft      = "draft"
	ItemCompaction = "compaction"
)

const (
	StepNote      = "note"
	StepThinking  = "thinking"
	StepTool      = "tool"
	StepWebSearch = "web_search"
)

type Attachment struct {
	ID        any `json:"id"`
	Name      any `json:"name"`
	MediaType any `json:"media_type"`
	Kind      any `json:"kind"`
}

type Citation struct {
	URL   string `json:"url"`
	Title string `json:"title"`
	Cited string `json:"cited"`
}

type SearchResult struct {
	URL   any `json:"url"`
	Title any `json:"title"`
}

type Step struct {
	Kind          string         `json:"kind"`
	Text          string         `json:"text"`
	ID            any            `json:"id"`
	Name          string         `json:"name"`
	Args          string         `json:"args"`
	State         *string        `json:"state"`
	Result        *string        `json:"result"`
	IsError       bool           `json:"is_error"`
	ChildThreadID any            `json:"child_thread_id"`
	Results       []SearchResult `json:"results"`
	Error         any            `json:"error"`
}

type Item struct {
	Seq       int64        `json:"seq"`
	RunID     *int64       `json:"run_id"`
	CreatedAt float64      `json:"created_at"`
	Kind      string       `json:"kind"`
	Text      string       `json:"text"`
	Files     []Attachment `json:"files"`
	Steps     []*Step      `json:"steps"`
	Citations []Citation   `json:"citations"`
	Draft     py.Dict      `json:"draft"`
}

func get(d py.Dict, key string, fallback any) any {
	if v, ok := d.Get(key); ok {
		return v
	}
	return fallback
}

func text(d py.Dict, key string) string {
	s, _ := get(d, key, "").(string)
	return s
}

func citationsOf(block py.Dict) []Citation {
	list, _ := get(block, "citations", nil).([]any)
	var found []Citation
	for _, raw := range list {
		c, _ := raw.(py.Dict)
		if url := text(c, "url"); url != "" {
			title := text(c, "title")
			if title == "" {
				title = url
			}
			found = append(found, Citation{URL: url, Title: title})
		} else if cited := text(c, "cited_text"); cited != "" {
			title := text(c, "document_title")
			if title == "" {
				title = "attachment"
			}
			found = append(found, Citation{Title: title, Cited: strings.TrimSpace(cited)})
		}
	}
	return found
}

func addAnswer(items []*Item, base Item, block py.Dict) []*Item {
	var last *Item
	if n := len(items); n > 0 && items[n-1].Kind == ItemAssistant && items[n-1].Seq == base.Seq {
		last = items[n-1]
	} else {
		item := base
		item.Kind = ItemAssistant
		last = &item
		items = append(items, last)
	}
	body := text(block, "text")
	for _, c := range citationsOf(block) {
		at := slices.Index(last.Citations, c)
		if at < 0 {
			last.Citations = append(last.Citations, c)
			at = len(last.Citations) - 1
		}
		trail := ""
		if strings.HasSuffix(body, " ") {
			trail = " "
		}
		body = strings.TrimRightFunc(body, py.IsSpace) + fmt.Sprintf(" [%d]", at+1) + trail
	}
	last.Text += body
	return items
}

func parsed(content string) py.Dict {
	v, err := py.JSONLoads([]byte(content))
	if err != nil {
		return py.Dict{}
	}
	d, ok := v.(py.Dict)
	if !ok {
		return py.Dict{}
	}
	return d
}

func draftKey(v any) (int64, bool) {
	switch n := v.(type) {
	case int64:
		return n, true
	case *big.Int:
		return n.Int64(), n.IsInt64()
	case float64:
		return int64(n), float64(int64(n)) == n
	case bool:
		if n {
			return 1, true
		}
		return 0, true
	}
	return 0, false
}

func Items(messages []journal.Message, calls []journal.ToolCall, drafts map[int64]py.Dict) ([]*Item, error) {
	byKey := make(map[journal.CallKey]journal.ToolCall, len(calls))
	for _, c := range calls {
		byKey[journal.CallKey{RunID: c.RunID, ToolUseID: c.ToolUseID}] = c
	}
	items := []*Item{}
	uses := map[any]*Step{}
	var attached []Attachment
	var activity *Item
	step := func(base Item, found *Step) *Step {
		if activity == nil {
			item := base
			item.Kind = ItemActivity
			item.Steps = []*Step{}
			activity = &item
			items = append(items, activity)
		}
		activity.Steps = append(activity.Steps, found)
		return found
	}
	for _, m := range messages {
		base := Item{Seq: m.Seq, RunID: m.RunID, CreatedAt: m.CreatedAt}
		hasUse, err := block.HasKind(m.Content, block.ToolUse)
		if err != nil {
			return nil, fmt.Errorf("service: message %d: %w", m.ID, err)
		}
		final := m.Role == journal.Assistant && !hasUse
		blocks, err := block.DecodeAll(m.Content)
		if err != nil {
			return nil, fmt.Errorf("service: message %d: %w", m.ID, err)
		}
		for _, b := range blocks {
			switch {
			case b.Kind == block.Attachment && m.Role == journal.User:
				name := b.String("name")
				if name == "" {
					name = "file"
				}
				attached = append(attached, Attachment{
					ID: b.Get("file_id"), Name: name, MediaType: b.String("media_type"), Kind: b.Get("kind"),
				})
			case b.Kind == block.Text && m.Role == journal.User:
				activity = nil
				item := base
				item.Kind, item.Text, item.Files = ItemUser, b.Text(), attached
				if item.Files == nil {
					item.Files = []Attachment{}
				}
				items = append(items, &item)
				attached = nil
			case b.Kind == block.Text && final:
				activity = nil
				items = addAnswer(items, base, b.Value)
			case b.Kind == block.Text:
				if body := b.Text(); strings.TrimSpace(body) != "" {
					step(base, &Step{Kind: StepNote, Text: body})
				}
			case b.Kind == block.Thinking || b.Kind == block.RedactedThinking:
				if body := b.String("thinking"); body != "" {
					step(base, &Step{Kind: StepThinking, Text: body})
				}
			case b.Kind == block.ToolUse || b.Kind == block.ServerToolUse:
				id := b.Get("id")
				args, err := shortArgs(b.Get("input"))
				if err != nil {
					return nil, err
				}
				found := &Step{Kind: StepTool, ID: id, Name: b.String("name"), Args: args}
				if s, ok := id.(string); ok && m.RunID != nil {
					if call, ok := byKey[journal.CallKey{RunID: *m.RunID, ToolUseID: s}]; ok {
						found.State = new(call.State)
					}
				}
				uses[id] = step(base, found)
			case b.Kind == block.ToolResult:
				found := uses[b.Get("tool_use_id")]
				content := b.Get("content")
				preview := progress.PreviewOf(content)
				isError := py.Truthy(b.Get("is_error"))
				if found == nil {
					step(base, &Step{Kind: StepTool, ID: b.Get("tool_use_id"), Name: "?", Result: &preview, IsError: isError})
					continue
				}
				found.Result, found.IsError = &preview, isError
				detail := py.Dict{}
				if s, ok := content.(string); ok {
					detail = parsed(s)
				}
				if found.Name == loopResearch {
					found.ChildThreadID = get(detail, "child_thread_id", nil)
				}
				if found.Name == draftCreate {
					if id, ok := draftKey(get(detail, "draft_id", nil)); ok {
						if draft, ok := drafts[id]; ok {
							item := base
							item.Kind, item.Draft = ItemDraft, draft
							items = append(items, &item)
						}
					}
				}
			case b.Kind == block.WebSearchResult:
				content := b.Get("content")
				results := []SearchResult{}
				if list, ok := content.([]any); ok {
					for _, r := range list {
						d, ok := r.(py.Dict)
						if !ok || !py.Truthy(get(d, "url", nil)) {
							continue
						}
						title := get(d, "title", nil)
						if !py.Truthy(title) {
							title = get(d, "url", nil)
						}
						results = append(results, SearchResult{URL: get(d, "url", nil), Title: title})
					}
				}
				if found := uses[b.Get("tool_use_id")]; found != nil {
					found.Result = new(fmt.Sprintf("%d results", len(results)))
				}
				var errorCode any
				if d, ok := content.(py.Dict); ok {
					errorCode = get(d, "error_code", nil)
				}
				step(base, &Step{Kind: StepWebSearch, Results: results, Error: errorCode})
			case b.Kind == block.Fallback:
				step(base, &Step{Kind: StepNote, Text: fallbackNote(b.Value)})
			case b.Kind == block.Compaction:
				activity = nil
				item := base
				item.Kind = ItemCompaction
				items = append(items, &item)
			}
		}
	}
	return items, nil
}

const loopResearch = "research"

func fallbackNote(block py.Dict) string {
	model := func(key string) string {
		d, _ := get(block, key, nil).(py.Dict)
		name, _ := get(d, "model", "").(string)
		return name
	}
	note := fmt.Sprintf("%s declined; %s took over", model("from"), model("to"))
	if trigger, ok := get(block, "trigger", nil).(py.Dict); ok {
		if category, _ := get(trigger, "category", "").(string); category != "" {
			note += fmt.Sprintf(" (refusal category: %s)", category)
		}
	}
	return note + "."
}

func shortArgs(input any) (string, error) {
	if !py.Truthy(input) {
		input = py.Dict{}
	}
	if _, ok := input.(py.Dict); !ok {
		return fmt.Sprint(input), nil
	}
	return progress.ShortArgs(json.RawMessage(py.JSONDumps(input)))
}

type Meter struct {
	Tokens    int64   `json:"tokens"`
	CompactAt int64   `json:"compact_at"`
	Limit     int64   `json:"limit"`
	Share     float64 `json:"share"`
}

func MeterOf(ctx context.Context, q db.Querier, thread threads.Thread) (Meter, error) {
	tuning, err := actx.ReadTuning(ctx, q)
	if err != nil {
		return Meter{}, err
	}
	limits := actx.LimitsFor(threads.ContextLimitOf(thread), tuning)
	tokens, err := usage.PromptTokens(ctx, q, thread.ID)
	if err != nil {
		return Meter{}, err
	}
	share := 1.0
	if limits.Compact != 0 {
		share = min(float64(tokens)/float64(limits.Compact), 1.0)
	}
	return Meter{Tokens: tokens, CompactAt: limits.Compact, Limit: limits.Hard, Share: share}, nil
}

func CostOfThread(ctx context.Context, q db.Querier, threadID int64) (float64, error) {
	var cost float64
	err := q.QueryRowContext(ctx,
		"SELECT COALESCE(SUM(cost_usd), 0) AS cost FROM usage WHERE thread_id = ? OR thread_id IN (SELECT id FROM threads WHERE parent_thread_id = ?)",
		threadID, threadID).Scan(&cost)
	return cost, err
}

func DraftsOf(ctx context.Context, q db.Querier, threadID int64) ([]py.Dict, error) {
	return rowDicts(ctx, q, "SELECT * FROM drafts WHERE thread_id = ? ORDER BY id", threadID)
}

func rowDicts(ctx context.Context, q db.Querier, query string, args ...any) ([]py.Dict, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	columns, err := rows.Columns()
	if err != nil {
		_ = rows.Close()
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (py.Dict, error) {
		values := make([]any, len(columns))
		ptrs := make([]any, len(columns))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if err := r.Scan(ptrs...); err != nil {
			return nil, err
		}
		d := make(py.Dict, len(columns))
		for i, c := range columns {
			v := values[i]
			if b, ok := v.([]byte); ok {
				v = string(b)
			}
			d[i] = py.Pair{Key: c, Value: v}
		}
		return d, nil
	})
}

type ThreadView struct {
	Thread          threads.Thread
	Items           []*Item
	Runs            []journal.Run
	Active          *journal.Run
	Queued          []journal.QueuedMessage
	Cost            float64
	Meter           Meter
	LastOperatorSeq *int64
	BranchedFrom    *threads.Thread
	Question        *Question
}

func View(ctx context.Context, q db.Querier, threadID int64) (*ThreadView, error) {
	thread, err := threads.Get(ctx, q, threadID)
	if err != nil || thread == nil {
		return nil, err
	}
	v := &ThreadView{Thread: *thread}
	history, err := journal.LoadHistory(ctx, q, threadID)
	if err != nil {
		return nil, err
	}
	rows, err := DraftsOf(ctx, q, threadID)
	if err != nil {
		return nil, err
	}
	drafts := make(map[int64]py.Dict, len(rows))
	for _, d := range rows {
		if id, ok := draftKey(get(d, "id", nil)); ok {
			drafts[id] = d
		}
	}
	if v.Items, err = Items(history.Messages, history.Calls, drafts); err != nil {
		return nil, err
	}
	if v.Runs, err = journal.RunsOf(ctx, q, threadID); err != nil {
		return nil, err
	}
	if v.Active, err = journal.ActiveRun(ctx, q, threadID); err != nil {
		return nil, err
	}
	if v.Queued, err = journal.Queued(ctx, q, threadID); err != nil {
		return nil, err
	}
	if v.Cost, err = CostOfThread(ctx, q, threadID); err != nil {
		return nil, err
	}
	if v.Meter, err = MeterOf(ctx, q, *thread); err != nil {
		return nil, err
	}
	last, err := history.LastOperator()
	if err != nil {
		return nil, err
	}
	if last != nil {
		v.LastOperatorSeq = &last.Seq
	}
	if v.Question, err = PendingQuestion(ctx, q, threadID); err != nil {
		return nil, err
	}
	if thread.BranchedFromThreadID != nil && *thread.BranchedFromThreadID != 0 {
		if v.BranchedFrom, err = threads.Get(ctx, q, *thread.BranchedFromThreadID); err != nil {
			return nil, err
		}
	}
	return v, nil
}

func RunDict(ctx context.Context, q db.Querier, runID int64) (py.Dict, error) {
	found, err := journal.GetRun(ctx, q, runID)
	if err != nil {
		return nil, err
	}
	if found == nil {
		return nil, fmt.Errorf("service: no run %d", runID)
	}
	run, err := rowDicts(ctx, q, "SELECT * FROM runs WHERE id = ?", runID)
	if err != nil {
		return nil, err
	}
	if len(run) == 0 {
		return nil, fmt.Errorf("service: run %d vanished", runID)
	}
	calls, err := rowDicts(ctx, q, "SELECT * FROM tool_calls WHERE run_id = ? ORDER BY id", runID)
	if err != nil {
		return nil, err
	}
	list := make([]any, len(calls))
	for i, c := range calls {
		list[i] = c
	}
	var cost any
	if err := q.QueryRowContext(ctx, "SELECT COALESCE(SUM(cost_usd), 0) FROM usage WHERE run_id = ?", runID).Scan(&cost); err != nil {
		return nil, err
	}
	skills, err := journal.SkillsOf(*found)
	if err != nil {
		return nil, err
	}
	return append(run[0], py.Pair{Key: "calls", Value: list}, py.Pair{Key: "cost", Value: cost}, py.Pair{Key: "skills", Value: skills}), nil
}

type Question struct {
	CallID    int64
	RunID     int64
	ToolUseID string
	Text      string
}

// PendingQuestion returns the parked run's unanswered ask_operator call.
func PendingQuestion(ctx context.Context, q db.Querier, threadID int64) (*Question, error) {
	var out Question
	var argsJSON string
	err := q.QueryRowContext(ctx,
		"SELECT c.id, c.run_id, c.tool_use_id, c.args_json FROM tool_calls c JOIN runs r ON r.id = c.run_id "+
			"WHERE r.thread_id = ? AND r.state = ? AND c.name = ? AND c.state = ? ORDER BY c.id DESC LIMIT 1",
		threadID, journal.Parked, "ask_operator", journal.Started).
		Scan(&out.CallID, &out.RunID, &out.ToolUseID, &argsJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var args struct {
		Question string `json:"question"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return nil, err
	}
	out.Text = args.Question
	return &out, nil
}

// Answer stores the operator's answer as the parked tool's result and resumes
// the run through the normal resume path.
func (s *Service) Answer(ctx context.Context, threadID int64, text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return fmt.Errorf("answer is required")
	}
	q, err := PendingQuestion(ctx, s.DB.Read, threadID)
	if err != nil {
		return err
	}
	if q == nil {
		return fmt.Errorf("no pending question")
	}
	result := py.Dict{
		{Key: "type", Value: block.ToolResult},
		{Key: "tool_use_id", Value: q.ToolUseID},
		{Key: "content", Value: text},
	}
	raw := json.RawMessage(py.JSONDumps(result, py.JSONOpts{Compact: true, NoEnsureASCII: true}))
	if _, err := journal.AppendMessage(ctx, s.DB, threadID, &q.RunID, journal.User, []json.RawMessage{raw}, s.now(), nil); err != nil {
		return err
	}
	if err := journal.FinishToolCall(ctx, s.DB.Write, q.CallID, journal.OK, s.now(), nil, nil); err != nil {
		return err
	}
	return s.StartResume(ctx, q.RunID)
}

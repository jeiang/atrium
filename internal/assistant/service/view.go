package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math/big"
	"slices"
	"strings"

	actx "github.com/jeiang/atrium/internal/assistant/context"
	"github.com/jeiang/atrium/internal/assistant/journal"
	"github.com/jeiang/atrium/internal/assistant/progress"
	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/assistant/usage"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/py"
)

const (
	serverToolUse   = "server_tool_use"
	webSearchResult = "web_search_tool_result"
	draftCreate     = "draft_email_create"
)

// codeExecutionResults are files.CODE_EXECUTION_RESULTS: legacy code execution results in old journals.
var codeExecutionResults = []string{"bash_code_execution_tool_result", "text_editor_code_execution_tool_result", "code_execution_tool_result"}

// Item kinds.
const (
	ItemUser       = "user"
	ItemActivity   = "activity"
	ItemAssistant  = "assistant"
	ItemDraft      = "draft"
	ItemCompaction = "compaction"
)

// Step kinds of an activity.
const (
	StepNote      = "note"
	StepThinking  = "thinking"
	StepTool      = "tool"
	StepWebSearch = "web_search"
)

// Attachment is a file the operator attached to a message.
type Attachment struct {
	ID        any
	Name      any
	MediaType any
	Kind      any
}

// Citation is one source of an answer: a web URL, or an attachment's title and the cited text.
type Citation struct {
	URL   string
	Title string
	Cited string
}

// SearchResult is one web_search result.
type SearchResult struct {
	URL   any
	Title any
}

// Step is one entry of a run's activity: a note, thinking, a tool call with its result, or a web search.
type Step struct {
	Kind          string
	Text          string
	ID            any
	Name          string
	Args          string
	State         *string
	Result        *string
	IsError       bool
	ChildThreadID any
	Results       []SearchResult
	Error         any
}

// Item is one entry of the conversation: an operator message, a run's activity, an answer, a draft card or
// a compaction marker.
type Item struct {
	Seq       int64
	RunID     *int64
	CreatedAt float64
	Kind      string
	Text      string
	Files     []Attachment
	Steps     []*Step
	Citations []Citation
	Draft     py.Dict
}

func blockKind(raw json.RawMessage) string {
	var b struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(raw, &b)
	return b.Type
}

func hasBlock(content []json.RawMessage, kind string) bool {
	return slices.ContainsFunc(content, func(b json.RawMessage) bool { return blockKind(b) == kind })
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

// citationsOf is citations_of: web citations carry a URL; document citations (attachments) carry the
// document title and the cited text.
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
			found = append(found, Citation{Title: title, Cited: py.Strip(cited)})
		}
	}
	return found
}

// addAnswer is add_answer: consecutive text blocks of one answer form one item; each cited span gets a [n]
// marker into its sources.
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

// parsed is parsed: a tool result's JSON object, else empty.
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

// executionResult is workspace.execution_result for a legacy code execution result block.
func executionResult(block py.Dict) (any, bool) {
	content, _ := get(block, "content", nil).(py.Dict)
	if v, ok := content.Get("error_code"); ok {
		return v, true
	}
	_, hasOut := content.Get("stdout")
	_, hasErr := content.Get("stderr")
	if hasOut || hasErr {
		out, _ := get(content, "stdout", nil).(string)
		if stderr, _ := get(content, "stderr", nil).(string); stderr != "" {
			if out != "" {
				out += "\n"
			}
			out += stderr
		}
		return out, py.Truthy(get(content, "return_code", nil))
	}
	rest := make(py.Dict, 0, len(content))
	for _, p := range content {
		if p.Key != "type" {
			rest = append(rest, p)
		}
	}
	return py.JSONDumps(rest, py.JSONOpts{NoEnsureASCII: true}), false
}

// Items is items_of: the conversation in order. Operator messages; one expandable activity per run (thinking,
// tool steps with their results, subagent spawns, searches, notes written between tool calls); the run's
// answers; mail cards for the drafts it created; compaction markers.
func Items(messages []journal.Message, calls map[journal.CallKey]journal.ToolCall, drafts map[int64]py.Dict) ([]*Item, error) {
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
		final := m.Role == journal.Assistant && !hasBlock(m.Content, actx.ToolUse)
		for _, raw := range m.Content {
			v, err := py.JSONLoads(raw)
			if err != nil {
				return nil, fmt.Errorf("service: message %d block: %w", m.ID, err)
			}
			block, _ := v.(py.Dict)
			kind, _ := get(block, "type", nil).(string)
			switch {
			case kind == actx.Attachment && m.Role == journal.User:
				attached = append(attached, Attachment{
					ID: get(block, "file_id", nil), Name: get(block, "name", "file"), MediaType: get(block, "media_type", ""), Kind: get(block, "kind", nil),
				})
			case kind == actx.Text && m.Role == journal.User:
				activity = nil
				item := base
				item.Kind, item.Text, item.Files = ItemUser, text(block, "text"), attached
				if item.Files == nil {
					item.Files = []Attachment{}
				}
				items = append(items, &item)
				attached = nil
			case kind == actx.Text && final:
				activity = nil
				items = addAnswer(items, base, block)
			case kind == actx.Text:
				if body := text(block, "text"); py.Strip(body) != "" {
					step(base, &Step{Kind: StepNote, Text: body})
				}
			case kind == actx.Thinking || kind == actx.RedactedThinking:
				if body := text(block, "thinking"); body != "" {
					step(base, &Step{Kind: StepThinking, Text: body})
				}
			case kind == actx.ToolUse || kind == serverToolUse:
				id := get(block, "id", nil)
				args, err := shortArgs(get(block, "input", nil))
				if err != nil {
					return nil, err
				}
				name, _ := get(block, "name", "").(string)
				found := &Step{Kind: StepTool, ID: id, Name: name, Args: args}
				if s, ok := id.(string); ok && m.RunID != nil {
					if call, ok := calls[journal.CallKey{RunID: *m.RunID, ToolUseID: s}]; ok {
						found.State = new(call.State)
					}
				}
				uses[id] = step(base, found)
			case kind == actx.ToolResult:
				found := uses[get(block, "tool_use_id", nil)]
				content := get(block, "content", "")
				preview := progress.PreviewOf(content)
				isError := py.Truthy(get(block, "is_error", nil))
				if found == nil {
					step(base, &Step{Kind: StepTool, ID: get(block, "tool_use_id", nil), Name: "?", Result: &preview, IsError: isError})
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
			case kind == webSearchResult:
				content := get(block, "content", nil)
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
				if found := uses[get(block, "tool_use_id", nil)]; found != nil {
					found.Result = new(fmt.Sprintf("%d results", len(results)))
				}
				var errorCode any
				if d, ok := content.(py.Dict); ok {
					errorCode = get(d, "error_code", nil)
				}
				step(base, &Step{Kind: StepWebSearch, Results: results, Error: errorCode})
			case slices.Contains(codeExecutionResults, kind):
				if found := uses[get(block, "tool_use_id", nil)]; found != nil {
					result, failed := executionResult(block)
					found.Result, found.IsError = new(progress.PreviewOf(result)), failed
				}
			case kind == actx.Compaction:
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

func shortArgs(input any) (string, error) {
	if !py.Truthy(input) {
		input = py.Dict{}
	}
	if _, ok := input.(py.Dict); !ok {
		return py.ReprValue(input), nil
	}
	return progress.ShortArgs(json.RawMessage(py.JSONDumps(input)))
}

// Meter is meter_of: the prompt tokens of the thread's last request against the point where compaction
// kicks in.
type Meter struct {
	Tokens    int64   `json:"tokens"`
	CompactAt int64   `json:"compact_at"`
	Limit     int64   `json:"limit"`
	Share     float64 `json:"share"`
}

// MeterOf is meter_of.
func MeterOf(ctx context.Context, q db.Querier, thread threads.Thread) (Meter, error) {
	limits := actx.LimitsFor(threads.ContextLimitOf(thread))
	var tokens int64
	err := q.QueryRowContext(ctx,
		"SELECT input_tokens + cache_write_5m + cache_write_1h + cache_read AS prompt FROM usage WHERE thread_id = ? ORDER BY id DESC LIMIT 1",
		thread.ID).Scan(&tokens)
	if err != nil && err != sql.ErrNoRows {
		return Meter{}, err
	}
	share := 1.0
	if limits.Compact != 0 {
		share = min(float64(tokens)/float64(limits.Compact), 1.0)
	}
	return Meter{Tokens: tokens, CompactAt: limits.Compact, Limit: limits.Hard, Share: share}, nil
}

// CostOfThread is cost_of_thread: the spend of the thread's runs including its subagents' runs.
func CostOfThread(ctx context.Context, q db.Querier, threadID int64) (float64, error) {
	var cost float64
	err := q.QueryRowContext(ctx,
		"SELECT COALESCE(SUM(cost_usd), 0) AS cost FROM usage WHERE thread_id = ? OR thread_id IN (SELECT id FROM threads WHERE parent_thread_id = ?)",
		threadID, threadID).Scan(&cost)
	return cost, err
}

// DraftsOf is workspace.drafts_of: the thread's drafts rows as column-to-value dicts, oldest first.
func DraftsOf(ctx context.Context, q db.Querier, threadID int64) ([]py.Dict, error) {
	return rowDicts(ctx, q, "SELECT * FROM drafts WHERE thread_id = ? ORDER BY id", threadID)
}

// rowDicts is [dict(r) for r in conn.execute(query, args)]: each row as its columns in table order.
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

// ThreadView is thread_view: what the chat page shows of a thread.
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
}

// View is thread_view: nil when there is no such thread.
func View(ctx context.Context, q db.Querier, threadID int64) (*ThreadView, error) {
	thread, err := threads.Get(ctx, q, threadID)
	if err != nil || thread == nil {
		return nil, err
	}
	v := &ThreadView{Thread: *thread}
	messages, err := journal.MessagesOf(ctx, q, threadID, false)
	if err != nil {
		return nil, err
	}
	calls, err := journal.ToolCallsForThread(ctx, q, threadID)
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
	if v.Items, err = Items(messages, calls, drafts); err != nil {
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
	last, err := journal.LastOperatorMessage(ctx, q, threadID)
	if err != nil {
		return nil, err
	}
	if last != nil {
		v.LastOperatorSeq = &last.Seq
	}
	if thread.BranchedFromThreadID != nil && *thread.BranchedFromThreadID != 0 {
		if v.BranchedFrom, err = threads.Get(ctx, q, *thread.BranchedFromThreadID); err != nil {
			return nil, err
		}
	}
	return v, nil
}

// RunView is run_view: the run row with its tool calls, its cost and the skills expanded into its message.
type RunView struct {
	journal.Run
	Calls  []journal.ToolCall `json:"calls"`
	Cost   float64            `json:"cost"`
	Skills []string           `json:"skills"`
}

// ViewRun is run_view: nil when there is no such run.
func ViewRun(ctx context.Context, q db.Querier, runID int64) (*RunView, error) {
	found, err := journal.GetRun(ctx, q, runID)
	if err != nil || found == nil {
		return nil, err
	}
	v := &RunView{Run: *found}
	if v.Calls, err = journal.ToolCallsOf(ctx, q, runID); err != nil {
		return nil, err
	}
	if v.Cost, err = usage.RunCost(ctx, q, runID); err != nil {
		return nil, err
	}
	if v.Skills, err = journal.SkillsOf(*found); err != nil {
		return nil, err
	}
	return v, nil
}

// Dict is the run_view dict as Python built it: the runs row in table column order, then calls (tool_calls rows
// in column order), cost and skills.
func (v *RunView) Dict(ctx context.Context, q db.Querier) (py.Dict, error) {
	run, err := rowDicts(ctx, q, "SELECT * FROM runs WHERE id = ?", v.ID)
	if err != nil {
		return nil, err
	}
	if len(run) == 0 {
		return nil, fmt.Errorf("service: run %d vanished", v.ID)
	}
	calls, err := rowDicts(ctx, q, "SELECT * FROM tool_calls WHERE run_id = ? ORDER BY id", v.ID)
	if err != nil {
		return nil, err
	}
	list := make([]any, len(calls))
	for i, c := range calls {
		list[i] = c
	}
	var cost any
	if err := q.QueryRowContext(ctx, "SELECT COALESCE(SUM(cost_usd), 0) FROM usage WHERE run_id = ?", v.ID).Scan(&cost); err != nil {
		return nil, err
	}
	return append(run[0], py.Pair{Key: "calls", Value: list}, py.Pair{Key: "cost", Value: cost}, py.Pair{Key: "skills", Value: v.Skills}), nil
}

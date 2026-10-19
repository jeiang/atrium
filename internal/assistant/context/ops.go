package context

import (
	gocontext "context"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"

	"github.com/jeiang/atrium/internal/assistant/journal"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/py"
)

// callsByUse is calls_by_use: tool_use ids are unique per thread; a resumed run journals results under a
// newer run than the call, so the newest call of an id wins.
func callsByUse(ctx gocontext.Context, q db.Querier, threadID int64) (map[string]journal.ToolCall, error) {
	all, err := journal.ToolCallsForThread(ctx, q, threadID)
	if err != nil {
		return nil, err
	}
	out := make(map[string]journal.ToolCall, len(all))
	for key, c := range all {
		if have, ok := out[key.ToolUseID]; !ok || c.ID > have.ID {
			out[key.ToolUseID] = c
		}
	}
	return out, nil
}

// Item is one tool result of the active context, as context_status lists it.
type Item struct {
	ID       string
	Seq      int64
	Tool     string
	Tokens   int64
	State    string
	ResultID *int64
}

// Dict is the item as the dashboard shows it.
func (i Item) Dict() py.Dict {
	var resultID any
	if i.ResultID != nil {
		resultID = *i.ResultID
	}
	return py.Dict{
		{Key: "id", Value: i.ID},
		{Key: "seq", Value: i.Seq},
		{Key: "tool", Value: i.Tool},
		{Key: "tokens", Value: i.Tokens},
		{Key: "state", Value: i.State},
		{Key: "result_id", Value: resultID},
	}
}

func resultContent(raw json.RawMessage) (any, error) {
	v, err := py.JSONLoads(raw)
	if err != nil {
		return nil, fmt.Errorf("context: content block: %w", err)
	}
	d, ok := v.(py.Dict)
	if !ok {
		return nil, fmt.Errorf("context: content block is %s, not an object", py.TypeName(v))
	}
	if c, ok := d.Get("content"); ok {
		return c, nil
	}
	return "", nil
}

func nonZero(p *int64) *int64 {
	if p == nil || *p == 0 {
		return nil
	}
	return p
}

// Items is items: every tool result of the active context outside folds, in journal order.
func Items(ctx gocontext.Context, q db.Querier, threadID int64) ([]Item, error) {
	p, err := GetProjection(ctx, q, threadID)
	if err != nil {
		return nil, err
	}
	ledger, err := LedgerRows(ctx, q, threadID)
	if err != nil {
		return nil, err
	}
	folds := ledger.withState(Folded, StagedFold)
	starts := map[int64]bool{}
	for _, f := range folds {
		starts[f.from()] = true
	}
	calls, err := callsByUse(ctx, q, threadID)
	if err != nil {
		return nil, err
	}
	tail, err := tailOf(ctx, q, threadID, p)
	if err != nil {
		return nil, err
	}
	var found []Item
	for _, m := range tail {
		if m.Role != journal.User || inFold(folds, m.Seq) || starts[m.Seq] {
			continue
		}
		for _, raw := range m.Content {
			h := headOf(raw)
			if h.Type != ToolResult {
				continue
			}
			call, hasCall := calls[h.ToolUseID]
			row, hasRow := ledger.Get(h.ToolUseID)
			item := Item{ID: h.ToolUseID, Seq: m.Seq, Tool: "?", State: Active}
			if hasRow {
				item.State = row.State
				item.ResultID = nonZero(row.ResultID)
			}
			if hasCall {
				item.Tool = call.Name
				if item.ResultID == nil {
					item.ResultID = nonZero(call.ResultID)
				}
			}
			content, err := resultContent(raw)
			if err != nil {
				return nil, err
			}
			item.Tokens = EstimateTokens(Serialize(content))
			found = append(found, item)
		}
	}
	return found, nil
}

func stagedOf(l Ledger) int64 {
	var sum int64
	for _, r := range l.withState(StagedDrop, StagedFold) {
		sum += r.Tokens
	}
	return sum
}

// StagedSavings is staged_savings: the tokens staged drops and folds would save once committed.
func StagedSavings(ctx gocontext.Context, q db.Querier, threadID int64) (int64, error) {
	l, err := LedgerRows(ctx, q, threadID)
	if err != nil {
		return 0, err
	}
	return stagedOf(l), nil
}

func estimateMessages(messages []Message) (int64, error) {
	var sum int64
	for _, m := range messages {
		n, err := EstimateContent(m.Content)
		if err != nil {
			return 0, err
		}
		sum += n
	}
	return sum, nil
}

// Status is status: the context_status dashboard.
func Status(ctx gocontext.Context, q db.Querier, threadID int64) (py.Dict, error) {
	active, _, err := ActiveMessages(ctx, q, threadID, "")
	if err != nil {
		return nil, err
	}
	approx, err := estimateMessages(active)
	if err != nil {
		return nil, err
	}
	found, err := Items(ctx, q, threadID)
	if err != nil {
		return nil, err
	}
	ledger, err := LedgerRows(ctx, q, threadID)
	if err != nil {
		return nil, err
	}
	folds := []any{}
	for _, r := range ledger.Rows {
		if r.Kind == ItemFold {
			folds = append(folds, py.Dict{
				{Key: "id", Value: r.ItemID},
				{Key: "from_seq", Value: optValue(r.FromSeq)},
				{Key: "to_seq", Value: optValue(r.ToSeq)},
				{Key: "tokens_saved", Value: r.Tokens},
				{Key: "state", Value: r.State},
			})
		}
	}
	messages, err := journal.MessagesOf(ctx, q, threadID, false)
	if err != nil {
		return nil, err
	}
	shown := found[max(len(found)-DashboardItems, 0):]
	items := make([]any, len(shown))
	for i, it := range shown {
		items[i] = it.Dict()
	}
	return py.Dict{
		{Key: "approx_tokens", Value: approx},
		{Key: "turn_start_seq", Value: TurnStart(messages)},
		{Key: "staged_savings", Value: stagedOf(ledger)},
		{Key: "commit_at_savings", Value: int64(CommitSavings)},
		{Key: "items", Value: items},
		{Key: "omitted_items", Value: int64(len(found) - len(shown))},
		{Key: "folds", Value: folds},
	}, nil
}

// StageOpts are stage's optional arguments.
type StageOpts struct {
	Tokens    int64
	ResultID  *int64
	Synopsis  *string
	SourceIDs []any
	FromSeq   *int64
	ToSeq     *int64
	Committed bool
}

// Stage is stage: it records an item's ledger state for this run (one row per run and item).
func Stage(ctx gocontext.Context, q db.Querier, runID int64, itemID, kind, state string, now float64, o StageOpts) error {
	sources := o.SourceIDs
	if sources == nil {
		sources = []any{}
	}
	var committed *float64
	if o.Committed {
		committed = &now
	}
	_, err := q.ExecContext(ctx,
		"INSERT INTO context_ledger (run_id, item_id, kind, result_id, source_ids_json, synopsis, tokens, state, staged_at, committed_at, from_seq, to_seq) "+
			"VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) "+
			"ON CONFLICT(run_id, item_id) DO UPDATE SET state = excluded.state, tokens = excluded.tokens, result_id = excluded.result_id, "+
			"synopsis = excluded.synopsis, source_ids_json = excluded.source_ids_json, staged_at = excluded.staged_at, committed_at = excluded.committed_at",
		runID, itemID, kind, o.ResultID, py.JSONDumps(sources, compactJSON), o.Synopsis, o.Tokens, state, now, committed, o.FromSeq, o.ToSeq)
	return err
}

// CleanIDs is clean_ids: ids (a decoded JSON value) must be a non-empty list of non-empty strings; repeats
// are removed, first occurrence kept.
func CleanIDs(ids any) ([]string, error) {
	bad := ContextError("ids must be a non-empty list of context item ids (see context_status)")
	list, ok := ids.([]any)
	if !ok || len(list) == 0 {
		return nil, bad
	}
	seen := map[string]bool{}
	var out []string
	for _, v := range list {
		s, ok := v.(string)
		if !ok || s == "" {
			return nil, bad
		}
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out, nil
}

func knownItems(ctx gocontext.Context, q db.Querier, threadID int64, ids []string) ([]Item, error) {
	found, err := Items(ctx, q, threadID)
	if err != nil {
		return nil, err
	}
	known := map[string]Item{}
	for _, it := range found {
		known[it.ID] = it
	}
	var missing []string
	out := make([]Item, 0, len(ids))
	for _, id := range ids {
		it, ok := known[id]
		if !ok {
			missing = append(missing, id)
		}
		out = append(out, it)
	}
	if len(missing) > 0 {
		return nil, ContextError("unknown context item(s): " + strings.Join(missing, ", "))
	}
	return out, nil
}

// Pin is pin: the items may no longer be dropped or folded by the agent.
func Pin(ctx gocontext.Context, q db.Querier, threadID, runID int64, ids any, now float64) (py.Dict, error) {
	clean, err := CleanIDs(ids)
	if err != nil {
		return nil, err
	}
	items, err := knownItems(ctx, q, threadID, clean)
	if err != nil {
		return nil, err
	}
	for _, it := range items {
		if it.State == Dropped || it.State == StagedDrop {
			return nil, ContextError(it.ID + " is dropped; recall it by result_id instead")
		}
		if err := Stage(ctx, q, runID, it.ID, ItemResult, Pinned, now, StageOpts{Tokens: it.Tokens, ResultID: it.ResultID, Committed: true}); err != nil {
			return nil, err
		}
	}
	return py.Dict{{Key: "pinned", Value: clean}}, nil
}

// Drop is drop: the items are staged for removal; they are applied by the next commit.
func Drop(ctx gocontext.Context, q db.Querier, threadID, runID int64, ids any, now float64) (py.Dict, error) {
	clean, err := CleanIDs(ids)
	if err != nil {
		return nil, err
	}
	items, err := knownItems(ctx, q, threadID, clean)
	if err != nil {
		return nil, err
	}
	for _, it := range items {
		if it.State == Pinned {
			return nil, ContextError(it.ID + " is pinned")
		}
		if it.State == Dropped || it.State == StagedDrop {
			continue
		}
		zero := int64(0)
		stub := py.Dict{{Key: "type", Value: ToolResult}, {Key: "tool_use_id", Value: it.ID}, {Key: "content", Value: dropStubValue(&zero)}}
		savings := max(it.Tokens-EstimateValue(stub), 0)
		if err := Stage(ctx, q, runID, it.ID, ItemResult, StagedDrop, now, StageOpts{Tokens: savings, ResultID: it.ResultID}); err != nil {
			return nil, err
		}
	}
	staged, err := StagedSavings(ctx, q, threadID)
	if err != nil {
		return nil, err
	}
	return py.Dict{
		{Key: "staged_drop", Value: clean},
		{Key: "staged_savings", Value: staged},
		{Key: "commit_at_savings", Value: int64(CommitSavings)},
	}, nil
}

// pyInt is isinstance(v, int) and not isinstance(v, bool) for a decoded JSON value.
func pyInt(v any) (*big.Int, bool) {
	switch v := v.(type) {
	case int64:
		return big.NewInt(v), true
	case int:
		return big.NewInt(int64(v)), true
	case *big.Int:
		return v, true
	}
	return nil, false
}

// Fold is fold: the agent's summary is staged to replace the completed messages fromSeq..toSeq (decoded JSON
// values, validated as Python does) once committed.
func Fold(ctx gocontext.Context, q db.Querier, threadID, runID int64, fromSeq, toSeq, summary any, sourceIDs []any, now float64) (py.Dict, error) {
	text, ok := summary.(string)
	if !ok || py.Strip(text) == "" {
		return nil, ContextError("summary is required")
	}
	for _, s := range sourceIDs {
		if _, isStr := s.(string); !isStr {
			if _, isInt := pyInt(s); !isInt {
				return nil, ContextError("source_ids must be strings or integers")
			}
		}
	}
	from, okFrom := pyInt(fromSeq)
	to, okTo := pyInt(toSeq)
	if !okFrom || !okTo || from.Cmp(to) > 0 {
		return nil, ContextError("from_seq and to_seq must be integers with from_seq <= to_seq")
	}
	p, err := GetProjection(ctx, q, threadID)
	if err != nil {
		return nil, err
	}
	messages, err := journal.MessagesOf(ctx, q, threadID, false)
	if err != nil {
		return nil, err
	}
	start := TurnStart(messages)
	bySeq := map[int64]journal.Message{}
	var last int64
	for _, m := range messages {
		if m.Seq >= p.BaseSeq {
			bySeq[m.Seq] = m
			last = max(last, m.Seq)
		}
	}
	first, okFirst := lookup(bySeq, from)
	end, okEnd := lookup(bySeq, to)
	if !okFirst || !okEnd {
		return nil, ContextError(fmt.Sprintf("the fold must lie within seq %d-%d of the active context", p.BaseSeq, last))
	}
	lo, hi := first.Seq, end.Seq
	if hi >= start {
		return nil, ContextError(fmt.Sprintf("only completed turns can be folded (seq < %d)", start))
	}
	if first.Role != journal.User || hasType(first.Content, ToolResult) {
		return nil, ContextError("from_seq must be an operator message, not a tool result")
	}
	if end.Role == journal.Assistant && hasType(end.Content, ToolUse) {
		return nil, ContextError("to_seq must not end on a tool call whose result follows it")
	}
	ledger, err := LedgerRows(ctx, q, threadID)
	if err != nil {
		return nil, err
	}
	for _, r := range ledger.Rows {
		if r.Kind == ItemFold && (r.State == Folded || r.State == StagedFold) && r.from() <= hi && lo <= r.to() {
			return nil, ContextError("overlaps fold " + r.ItemID)
		}
	}
	var inside int64
	for _, m := range messages {
		if m.Seq < lo || m.Seq > hi {
			continue
		}
		for _, raw := range m.Content {
			h := headOf(raw)
			if h.Type != ToolResult {
				continue
			}
			if r, ok := ledger.Get(h.ToolUseID); ok && r.State == Pinned {
				return nil, ContextError(h.ToolUseID + " is pinned; unpin is not available, choose a smaller range")
			}
		}
		n, err := EstimateContent(m.Content)
		if err != nil {
			return nil, err
		}
		inside += n
	}
	itemID := fmt.Sprintf("fold:%d-%d", lo, hi)
	savings := max(inside-EstimateTokens(text), 0)
	synopsis := py.Strip(text)
	sources := append([]any{}, sourceIDs...)
	if err := Stage(ctx, q, runID, itemID, ItemFold, StagedFold, now,
		StageOpts{Tokens: savings, Synopsis: &synopsis, SourceIDs: sources, FromSeq: &lo, ToSeq: &hi}); err != nil {
		return nil, err
	}
	staged, err := StagedSavings(ctx, q, threadID)
	if err != nil {
		return nil, err
	}
	return py.Dict{
		{Key: "staged_fold", Value: itemID},
		{Key: "tokens_saved", Value: savings},
		{Key: "staged_savings", Value: staged},
		{Key: "commit_at_savings", Value: int64(CommitSavings)},
	}, nil
}

func lookup(bySeq map[int64]journal.Message, seq *big.Int) (journal.Message, bool) {
	if !seq.IsInt64() {
		return journal.Message{}, false
	}
	m, ok := bySeq[seq.Int64()]
	return m, ok
}

// LoopHasThinking is loop_has_thinking: whether an assistant turn after start (the current tool loop)
// carries thinking.
func LoopHasThinking(messages []journal.Message, start int64) bool {
	for _, m := range messages {
		if m.Role != journal.Assistant || m.Seq <= start {
			continue
		}
		for _, raw := range m.Content {
			if isThinking(headOf(raw).Type) {
				return true
			}
		}
	}
	return false
}

// Committed is what Commit applied.
type Committed struct {
	ProjectionID int64
	SavedTokens  int64
	Applied      []string
}

// Commit is commit: it applies staged drops and folds to completed turns and writes a commit projection,
// or returns nil when nothing applied. Outside a natural boundary this is only safe while the current tool
// loop carries no signed thinking: editing the prefix invalidates every later thinking block.
func Commit(ctx gocontext.Context, q db.Querier, threadID, runID int64, now float64, boundary bool) (*Committed, error) {
	ledger, err := LedgerRows(ctx, q, threadID)
	if err != nil {
		return nil, err
	}
	staged := ledger.withState(StagedDrop, StagedFold)
	if len(staged) == 0 {
		return nil, nil
	}
	messages, err := journal.MessagesOf(ctx, q, threadID, false)
	if err != nil {
		return nil, err
	}
	start := TurnStart(messages)
	if !boundary && LoopHasThinking(messages, start) {
		return nil, nil
	}
	itemSeq := map[string]int{}
	for i, m := range messages {
		if m.Role != journal.User {
			continue
		}
		for _, raw := range m.Content {
			if h := headOf(raw); h.Type == ToolResult {
				itemSeq[h.ToolUseID] = i
			}
		}
	}
	calls, err := callsByUse(ctx, q, threadID)
	if err != nil {
		return nil, err
	}
	var saved int64
	var applied []string
	var editSeq *int64
	for _, r := range staged {
		var target, first int64
		var resultID *int64
		state := Folded
		if r.Kind == ItemFold {
			target, first = r.to(), r.from()
		} else {
			i, ok := itemSeq[r.ItemID]
			if !ok {
				continue
			}
			m := messages[i]
			target, first, state = m.Seq, m.Seq, Dropped
			if call, ok := calls[r.ItemID]; ok {
				resultID = call.ResultID
				if resultID == nil {
					if resultID, err = storeDropped(ctx, q, call, m, r.ItemID, now); err != nil {
						return nil, err
					}
				}
			}
		}
		if target >= start {
			continue
		}
		if _, err := q.ExecContext(ctx,
			"UPDATE context_ledger SET state = ?, committed_at = ?, result_id = COALESCE(?, result_id) WHERE id = ?",
			state, now, resultID, r.ID); err != nil {
			return nil, err
		}
		saved += r.Tokens
		applied = append(applied, r.ItemID)
		if editSeq == nil || first < *editSeq {
			editSeq = &first
		}
	}
	if len(applied) == 0 {
		return nil, nil
	}
	p, err := GetProjection(ctx, q, threadID)
	if err != nil {
		return nil, err
	}
	stripFrom := *editSeq
	if p.StripBeforeSeq != 0 {
		stripFrom = min(p.StripFromSeq, *editSeq)
	}
	id, err := SaveProjection(ctx, q, threadID, runID, KindCommit, p.BaseSeq, stripFrom, max(p.StripBeforeSeq, start), p.Head, saved, now)
	if err != nil {
		return nil, err
	}
	return &Committed{ProjectionID: id, SavedTokens: saved, Applied: applied}, nil
}

// storeDropped stores a dropped result that was sent whole (never stubbed), so recall can page it back.
func storeDropped(ctx gocontext.Context, q db.Querier, call journal.ToolCall, m journal.Message, useID string, now float64) (*int64, error) {
	for _, raw := range m.Content {
		if h := headOf(raw); h.Type != ToolResult || h.ToolUseID != useID {
			continue
		}
		content, err := resultContent(raw)
		if err != nil {
			return nil, err
		}
		id, _, err := Store(ctx, q, call.RunID, call.ID, call.Name, Serialize(content), now)
		if err != nil {
			return nil, err
		}
		if _, err := q.ExecContext(ctx, "UPDATE tool_calls SET result_id = ? WHERE id = ?", id, call.ID); err != nil {
			return nil, err
		}
		return &id, nil
	}
	return nil, fmt.Errorf("context: no tool_result for %s in message %d", useID, m.Seq)
}

func optValue(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}

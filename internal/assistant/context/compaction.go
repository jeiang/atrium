package context

import (
	gocontext "context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/jeiang/atrium/internal/anthropic"
	"github.com/jeiang/atrium/internal/assistant/journal"
	"github.com/jeiang/atrium/internal/assistant/usage"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/py"
)

// Manifest is manifest: the text that tells the model, after a compaction, what the ledger holds and which
// stored results it can page back.
func Manifest(ctx gocontext.Context, q db.Querier, threadID int64) (string, error) {
	ledger, err := LedgerRows(ctx, q, threadID)
	if err != nil {
		return "", err
	}
	found, err := Items(ctx, q, threadID)
	if err != nil {
		return "", err
	}
	tools := map[string]string{}
	for _, it := range found {
		tools[it.ID] = it.Tool
	}
	lines := []string{"Context manifest (maintained by atrium; ids are exact):"}
	if pinned := ledger.withState(Pinned); len(pinned) > 0 {
		parts := make([]string, len(pinned))
		for i, r := range pinned {
			parts[i] = r.ItemID
			if tool, ok := tools[r.ItemID]; ok {
				parts[i] = fmt.Sprintf("%s (%s)", r.ItemID, tool)
			}
		}
		lines = append(lines, "Pinned: "+strings.Join(parts, ", "))
	}
	if dropped := ledger.withState(Dropped); len(dropped) > 0 {
		parts := make([]string, len(dropped))
		for i, r := range dropped {
			parts[i] = fmt.Sprintf("%s -> result_id %s", r.ItemID, optInt(r.ResultID))
		}
		lines = append(lines, "Dropped (page back with recall): "+strings.Join(parts, ", "))
	}
	for _, r := range ledger.withState(Folded) {
		sources, err := r.sources()
		if err != nil {
			return "", err
		}
		line := fmt.Sprintf("Fold seq %s-%s: %s", optInt(r.FromSeq), optInt(r.ToSeq), optStr(r.Synopsis))
		if len(sources) > 0 {
			line += " [sources: " + joinStr(sources) + "]"
		}
		lines = append(lines, line)
	}
	rows, err := q.QueryContext(ctx,
		"SELECT r.result_id, r.tool, r.tokens FROM tool_results r JOIN runs ON runs.id = r.run_id WHERE runs.thread_id = ? ORDER BY r.result_id DESC LIMIT ?",
		threadID, ManifestResults)
	if err != nil {
		return "", err
	}
	stored, err := db.Collect(rows, func(r *sql.Rows) (string, error) {
		var id, tokens int64
		var tool string
		err := r.Scan(&id, &tool, &tokens)
		return fmt.Sprintf("%d (%s, ~%d tokens)", id, tool, tokens), err
	})
	if err != nil {
		return "", err
	}
	if len(stored) > 0 {
		lines = append(lines, "Stored results (recall(result_id, offset, limit)): "+strings.Join(stored, ", "))
	}
	lines = append(lines, "context_status() shows the live dashboard; context_pin/context_drop/context_fold manage it.")
	return strings.Join(lines, "\n"), nil
}

// ManifestBlock is manifest_block: the manifest as a text block.
func ManifestBlock(ctx gocontext.Context, q db.Querier, threadID int64) (json.RawMessage, error) {
	text, err := Manifest(ctx, q, threadID)
	if err != nil {
		return nil, err
	}
	return textBlock(text), nil
}

// unitStart is unit_start: where a re-inserted tail may begin: an assistant turn (its tool_uses are answered
// by the next message) or a user message without tool_results (operator text). Starting at a tool_result
// would leave it dangling.
func unitStart(m Message) bool {
	return m.Role == journal.Assistant || !hasType(m.Content, ToolResult)
}

// Preserved is preserved: the last PreserveMessages sent messages, moved forward to the next complete unit.
// Thinking is removed from re-inserted assistant turns: threshold compaction requires it, and adaptive
// thinking needs none.
func Preserved(sent []Message) []Message {
	start := max(len(sent)-PreserveMessages, 0)
	for start < len(sent) && !unitStart(sent[start]) {
		start++
	}
	var kept []Message
	for _, m := range sent[start:] {
		content := append([]json.RawMessage{}, m.Content...)
		if m.Role == journal.Assistant {
			content = StripThinking(m.Content)
		}
		if len(content) > 0 {
			kept = append(kept, Message{Role: m.Role, Content: content})
		}
	}
	return kept
}

// ServerCompacted is server_compacted, called after a `compaction` stop: the journaled compaction blocks,
// the manifest and the last complete units of what was sent become the new head.
func ServerCompacted(ctx gocontext.Context, q db.Querier, threadID, runID int64, blocks []json.RawMessage, sent []Message, now float64) (int64, error) {
	if _, err := Commit(ctx, q, threadID, runID, now, true); err != nil {
		return 0, err
	}
	manifest, err := ManifestBlock(ctx, q, threadID)
	if err != nil {
		return 0, err
	}
	head := append([]Message{
		{Role: journal.Assistant, Content: blocks},
		{Role: journal.User, Content: []json.RawMessage{manifest}},
	}, Preserved(sent)...)
	base, err := nextSeq(ctx, q, threadID)
	if err != nil {
		return 0, err
	}
	return SaveProjection(ctx, q, threadID, runID, KindServer, base, base, base, head, 0, now)
}

func nextSeq(ctx gocontext.Context, q db.Querier, threadID int64) (int64, error) {
	last, err := journal.LastMessage(ctx, q, threadID)
	if err != nil {
		return 0, err
	}
	if last == nil {
		return 0, fmt.Errorf("context: thread %d has no messages to compact", threadID)
	}
	return last.Seq + 1, nil
}

func blockDict(raw json.RawMessage) (py.Dict, error) {
	v, err := py.JSONLoads(raw)
	if err != nil {
		return nil, fmt.Errorf("context: content block: %w", err)
	}
	d, ok := v.(py.Dict)
	if !ok {
		return nil, fmt.Errorf("context: content block is %s, not an object", py.TypeName(v))
	}
	return d, nil
}

func getOr(d py.Dict, key string, fallback any) any {
	if v, ok := d.Get(key); ok {
		return v
	}
	return fallback
}

// flatten is flatten: the summary request carries no `tools`, so every non-text block is rendered as text.
func flatten(b py.Dict) py.Dict {
	kind := getOr(b, "type", nil)
	switch kind {
	case Text:
		return py.Dict{{Key: "type", Value: Text}, {Key: "text", Value: getOr(b, "text", "")}}
	case Compaction:
		return py.Dict{{Key: "type", Value: Text}, {Key: "text", Value: "[earlier context summary] " + str(getOr(b, "content", ""))}}
	}
	rest := make(py.Dict, 0, len(b))
	for _, p := range b {
		if p.Key != "type" && p.Key != "cache_control" {
			rest = append(rest, p)
		}
	}
	return py.Dict{{Key: "type", Value: Text}, {Key: "text", Value: "[" + str(kind) + "] " + Serialize(rest)}}
}

type shrunk struct {
	role    string
	content []py.Dict
}

func dictValues(content []py.Dict) []any {
	out := make([]any, len(content))
	for i, b := range content {
		out[i] = b
	}
	return out
}

// Shrink is shrink: a thinking-free, text-only copy of the messages for the summary request, with long tool
// results truncated until the request fits limit. Roles alternate and the first message is a user message.
func Shrink(messages []Message, limit int64) ([]Message, error) {
	var out []shrunk
	for _, m := range messages {
		var content []py.Dict
		for _, raw := range m.Content {
			b, err := blockDict(raw)
			if err != nil {
				return nil, err
			}
			if kind, _ := b.Get("type"); kind == Thinking || kind == RedactedThinking {
				continue
			}
			content = append(content, append(py.Dict{}, b...))
		}
		if len(content) == 0 {
			continue
		}
		if n := len(out); n > 0 && out[n-1].role == m.Role {
			out[n-1].content = append(out[n-1].content, content...)
		} else {
			out = append(out, shrunk{role: m.Role, content: content})
		}
	}
	var total int64
	for _, m := range out {
		total += EstimateValue(dictValues(m.content))
	}
	for _, m := range out {
		if total <= limit {
			break
		}
		for i, b := range m.content {
			if kind, _ := b.Get("type"); kind != ToolResult {
				continue
			}
			body, ok := getOr(b, "content", nil).(string)
			if !ok || utf8.RuneCountInString(body) <= PreviewChars {
				continue
			}
			cut := py.Clip(body, PreviewChars) + compactionTruncate
			total -= EstimateTokens(body) - EstimateTokens(cut)
			m.content[i] = b.Set("content", cut)
		}
	}
	result := make([]Message, 0, len(out)+1)
	for _, m := range out {
		content := make([]json.RawMessage, len(m.content))
		for i, b := range m.content {
			content[i] = encode(flatten(b))
		}
		result = append(result, Message{Role: m.role, Content: content})
	}
	if len(result) > 0 && result[0].Role == journal.Assistant {
		lead := Message{Role: journal.User, Content: []json.RawMessage{textBlock("[conversation continues from an earlier compaction]")}}
		result = append([]Message{lead}, result...)
	}
	return result, nil
}

// lastOperatorBlocks is last_operator_blocks: the text and attachment blocks of the latest operator message.
func lastOperatorBlocks(sent []Message) []json.RawMessage {
	for i := len(sent) - 1; i >= 0; i-- {
		m := sent[i]
		if m.Role != journal.User || !hasType(m.Content, Text) {
			continue
		}
		var out []json.RawMessage
		for _, raw := range m.Content {
			if kind := headOf(raw).Type; kind == Text || kind == Attachment {
				out = append(out, raw)
			}
		}
		return out
	}
	return nil
}

// SummaryParams is the summary request of ClientCompact: low effort, no tools, no thinking.
func SummaryParams(model string, active []Message, hardLimit int64) (anthropic.Params, error) {
	messages, err := Shrink(active, hardLimit-SummaryMaxTokens-EstimateTokens(SummaryRequest)-1000)
	if err != nil {
		return anthropic.Params{}, err
	}
	ask := textBlock(SummaryRequest)
	if n := len(messages); n > 0 && messages[n-1].Role == journal.User {
		messages[n-1].Content = append(messages[n-1].Content, ask)
	} else {
		messages = append(messages, Message{Role: journal.User, Content: []json.RawMessage{ask}})
	}
	wire := make([]json.RawMessage, len(messages))
	for i, m := range messages {
		wire[i] = m.Wire()
	}
	return anthropic.Params{
		Model:        model,
		MaxTokens:    SummaryMaxTokens,
		OutputConfig: &anthropic.OutputConfig{Effort: SummaryEffort},
		System:       encode(SummarySystem),
		Messages:     wire,
	}, nil
}

// ClientCompact is client_compact: one low-effort, tool-free, thinking-free summarization request; the
// journal stays intact and the new projection is summary + manifest + the operator's latest message (as
// sent, so its date stamp is kept). The call is metered as a compaction; a failed call fails the caller.
func ClientCompact(ctx gocontext.Context, q db.Querier, client *anthropic.Client, model string, active []Message, threadID, runID int64, kind string, hardLimit int64, now float64) (int64, error) {
	params, err := SummaryParams(model, active, hardLimit)
	if err != nil {
		return 0, err
	}
	response, err := client.Create(ctx, params)
	if err != nil {
		return 0, err
	}
	entry := usage.Entry{TS: now, ThreadID: threadID, RunID: runID, Kind: kind, Model: model, Effort: SummaryEffort, Compaction: 1}
	if _, err := usage.Record(ctx, q, entry, response.Usage); err != nil {
		return 0, err
	}
	var texts []string
	for _, raw := range response.Content {
		var b struct {
			Type string  `json:"type"`
			Text *string `json:"text"`
		}
		if json.Unmarshal(raw, &b) == nil && b.Type == Text {
			text := ""
			if b.Text != nil {
				text = *b.Text
			}
			texts = append(texts, text)
		}
	}
	summary := py.Strip(strings.Join(texts, "\n"))
	if summary == "" {
		return 0, ContextError("compaction summary was empty")
	}
	manifest, err := ManifestBlock(ctx, q, threadID)
	if err != nil {
		return 0, err
	}
	content := []json.RawMessage{textBlock("[Context summary written at compaction; earlier messages are no longer shown]\n" + summary), manifest}
	content = append(content, lastOperatorBlocks(active)...)
	base, err := nextSeq(ctx, q, threadID)
	if err != nil {
		return 0, err
	}
	before, err := estimateMessages(active)
	if err != nil {
		return 0, err
	}
	after, err := EstimateContent(content)
	if err != nil {
		return 0, err
	}
	head := []Message{{Role: journal.User, Content: content}}
	return SaveProjection(ctx, q, threadID, runID, KindClient, base, base, base, head, max(before-after, 0), now)
}

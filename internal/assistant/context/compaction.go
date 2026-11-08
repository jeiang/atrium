package context

import (
	gocontext "context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/jeiang/atrium/internal/anthropic"
	"github.com/jeiang/atrium/internal/assistant/block"
	"github.com/jeiang/atrium/internal/assistant/journal"
	"github.com/jeiang/atrium/internal/assistant/usage"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/py"
)

func manifest(ctx gocontext.Context, q db.Querier, threadID int64, t Tuning) (string, error) {
	ledger, err := loadLedger(ctx, q, threadID)
	if err != nil {
		return "", err
	}
	found, err := itemsOf(ctx, q, threadID, ledger)
	if err != nil {
		return "", err
	}
	tools := map[string]string{}
	for _, it := range found {
		tools[it.ID] = it.Tool
	}
	lines := []string{"Context manifest (maintained by atrium; ids are exact):"}
	if pinned := ledger.withState(StatePinned); len(pinned) > 0 {
		parts := make([]string, len(pinned))
		for i, e := range pinned {
			parts[i] = e.ID
			if tool, ok := tools[e.ID]; ok {
				parts[i] = fmt.Sprintf("%s (%s)", e.ID, tool)
			}
		}
		lines = append(lines, "Pinned: "+strings.Join(parts, ", "))
	}
	if dropped := ledger.withState(StateDropped); len(dropped) > 0 {
		parts := make([]string, len(dropped))
		for i, e := range dropped {
			parts[i] = fmt.Sprintf("%s -> result_id %d", e.ID, deref(e.ResultID))
		}
		lines = append(lines, "Dropped (page back with recall): "+strings.Join(parts, ", "))
	}
	for _, e := range ledger.withState(StateFolded) {
		line := fmt.Sprintf("Fold seq %d-%d: %s", e.Fold.From, e.Fold.To, e.Fold.Synopsis)
		if len(e.Fold.Sources) > 0 {
			line += " [sources: " + joinStr(e.Fold.Sources) + "]"
		}
		lines = append(lines, line)
	}
	rows, err := q.QueryContext(ctx,
		"SELECT r.result_id, r.tool, r.tokens FROM tool_results r JOIN runs ON runs.id = r.run_id WHERE runs.thread_id = ? ORDER BY r.result_id DESC LIMIT ?",
		threadID, t.ManifestResults)
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
	return strings.Join(lines, "\n"), nil
}

func manifestBlock(ctx gocontext.Context, q db.Querier, threadID int64, t Tuning) (json.RawMessage, error) {
	text, err := manifest(ctx, q, threadID, t)
	if err != nil {
		return nil, err
	}
	return TextBlock(text), nil
}

func unitStart(m Message) (bool, error) {
	if m.Role == journal.Assistant {
		return true, nil
	}
	has, err := hasType(m.Content, block.ToolResult)
	return !has, err
}

func preserved(sent []Message, t Tuning) ([]Message, error) {
	start := max(len(sent)-t.PreserveMessages, 0)
	for start < len(sent) {
		ok, err := unitStart(sent[start])
		if err != nil {
			return nil, err
		}
		if ok {
			break
		}
		start++
	}
	var kept []Message
	for _, m := range sent[start:] {
		var content []json.RawMessage
		if m.Role == journal.Assistant {
			var err error
			if content, err = StripThinking(m.Content); err != nil {
				return nil, err
			}
		} else {
			content = append(content, m.Content...)
		}
		if len(content) > 0 {
			kept = append(kept, Message{Role: m.Role, Content: content})
		}
	}
	return kept, nil
}

func ServerCompacted(ctx gocontext.Context, q db.Querier, threadID, runID int64, blocks []json.RawMessage, sent []Message, now float64, t Tuning) (int64, error) {
	body, err := manifestBlock(ctx, q, threadID, t)
	if err != nil {
		return 0, err
	}
	kept, err := preserved(sent, t)
	if err != nil {
		return 0, err
	}
	head := append([]Message{
		{Role: journal.Assistant, Content: blocks},
		{Role: journal.User, Content: []json.RawMessage{body}},
	}, kept...)
	base, err := nextSeq(ctx, q, threadID)
	if err != nil {
		return 0, err
	}
	return saveProjection(ctx, q, threadID, runID, base, base, base, head, 0, now)
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
	return py.Dict{{Key: "type", Value: Text}, {Key: "text", Value: "[" + str(kind) + "] " + serialize(rest)}}
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

func shrink(messages []Message, limit int64, previewChars int) ([]Message, error) {
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
			if !ok || utf8.RuneCountInString(body) <= previewChars {
				continue
			}
			cut := py.Clip(body, previewChars) + compactionTruncate
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
		lead := Message{Role: journal.User, Content: []json.RawMessage{TextBlock("[conversation continues from an earlier compaction]")}}
		result = append([]Message{lead}, result...)
	}
	return result, nil
}

func lastOperatorBlocks(sent []Message) ([]json.RawMessage, error) {
	for i := len(sent) - 1; i >= 0; i-- {
		m := sent[i]
		if m.Role != journal.User {
			continue
		}
		has, err := hasType(m.Content, block.Text)
		if err != nil {
			return nil, err
		}
		if !has {
			continue
		}
		blocks, err := block.DecodeAll(m.Content)
		if err != nil {
			return nil, err
		}
		var out []json.RawMessage
		for _, b := range blocks {
			if b.Kind == block.Text || b.Kind == block.Attachment {
				out = append(out, b.Raw)
			}
		}
		return out, nil
	}
	return nil, nil
}

func summaryParams(model string, active []Message, hardLimit int64, t Tuning) (anthropic.Params, error) {
	request := SummaryRequest(t.Summary)
	messages, err := shrink(active, hardLimit-int64(t.SummaryMaxTokens)-EstimateTokens(request)-1000, t.PreviewChars)
	if err != nil {
		return anthropic.Params{}, err
	}
	ask := TextBlock(request)
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
		MaxTokens:    int64(t.SummaryMaxTokens),
		OutputConfig: &anthropic.OutputConfig{Effort: SummaryEffort},
		System:       encode(SummarySystem),
		Messages:     wire,
	}, nil
}

func ClientCompact(ctx gocontext.Context, d *db.DB, client *anthropic.Client, model string, active []Message, threadID, runID int64, kind string, hardLimit int64, now float64, t Tuning) (int64, error) {
	params, err := summaryParams(model, active, hardLimit, t)
	if err != nil {
		return 0, err
	}
	response, err := client.Create(ctx, params)
	if err != nil {
		return 0, err
	}
	entry := usage.Entry{TS: now, ThreadID: threadID, RunID: runID, Kind: kind, Model: model, Effort: SummaryEffort, Compaction: 1}
	if _, err := usage.Record(ctx, d, entry, response.Usage); err != nil {
		return 0, err
	}
	blocks, err := block.DecodeAll(response.Content)
	if err != nil {
		return 0, err
	}
	var texts []string
	for _, b := range blocks {
		if b.Kind == block.Text {
			texts = append(texts, b.Text())
		}
	}
	summary := py.Strip(strings.Join(texts, "\n"))
	if summary == "" {
		return 0, ContextError("compaction summary was empty")
	}
	manifest, err := manifestBlock(ctx, d.Read, threadID, t)
	if err != nil {
		return 0, err
	}
	content := []json.RawMessage{TextBlock("[Context summary written at compaction; earlier messages are no longer shown]\n" + summary), manifest}
	last, err := lastOperatorBlocks(active)
	if err != nil {
		return 0, err
	}
	content = append(content, last...)
	base, err := nextSeq(ctx, d.Read, threadID)
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
	return saveProjection(ctx, d.Write, threadID, runID, base, base, base, head, max(before-after, 0), now)
}

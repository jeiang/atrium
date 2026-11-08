package context

import (
	gocontext "context"
	"encoding/json"
	"fmt"
	"unicode/utf8"

	"github.com/jeiang/atrium/internal/anthropic"
	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/assistant/block"
	"github.com/jeiang/atrium/internal/assistant/journal"
	"github.com/jeiang/atrium/internal/assistant/prompts"
	"github.com/jeiang/atrium/internal/assistant/usage"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/textx"
)

func Supersede(ctx gocontext.Context, q db.Querier, threadID, fromSeq int64) error {
	if _, err := q.ExecContext(ctx,
		"DELETE FROM context_ledger WHERE state IN (?, ?) AND run_id IN (SELECT id FROM runs WHERE thread_id = ? AND superseded = 1)",
		StagedDrop, StagedFold, threadID); err != nil {
		return err
	}
	_, err := q.ExecContext(ctx, "DELETE FROM context_projections WHERE thread_id = ? AND base_seq > ?", threadID, fromSeq)
	return err
}

const (
	CharsPerToken = 4
	StubNote      = "Result stored; call recall(result_id, offset, limit) to read it in pages."
)

const (
	ClearToolUses    = "clear_tool_uses_20250919"
	Compaction       = block.Compaction
	Text             = block.Text
	ToolUse          = block.ToolUse
	ToolResult       = block.ToolResult
	Thinking         = block.Thinking
	RedactedThinking = block.RedactedThinking
	Attachment       = block.Attachment
	recallTool       = "recall"
)

const (
	ItemResult = "result"
	ItemFold   = "fold"
	Active     = "active"
	Pinned     = "pinned"
	StagedDrop = "staged_drop"
	StagedFold = "staged_fold"
	Dropped    = "dropped"
	Folded     = "folded"
	KindCommit = "commit"
)

type ContextError string

func (e ContextError) Error() string { return string(e) }

var (
	compactJSON = py.JSONOpts{Compact: true, NoEnsureASCII: true}
	valueJSON   = py.JSONOpts{NoEnsureASCII: true}
)

func EstimateTokens(text string) int64 {
	return (int64(utf8.RuneCountInString(text)) + CharsPerToken - 1) / CharsPerToken
}

func EstimateValue(v any) int64 { return EstimateTokens(py.JSONDumps(v, valueJSON)) }

func EstimateContent(content []json.RawMessage) (int64, error) {
	values, err := decodeAll(content)
	if err != nil {
		return 0, err
	}
	return EstimateValue(values), nil
}

func serialize(value any) string {
	if s, ok := value.(string); ok {
		return s
	}
	return py.JSONDumps(value, valueJSON)
}

type Tuning struct {
	ClearShare        float64
	ClearAtLeastShare float64
	CompactShare      float64
	PreflightShare    float64
	ClearKeep         int
	PreserveMessages  int
	SummaryMaxTokens  int
	StubTokens        int
	PreviewChars      int
	ManifestResults   int
	Summary           string
}

func ReadTuning(ctx gocontext.Context, q db.Querier) (Tuning, error) {
	r := assistant.Read(ctx, q)
	t := Tuning{
		ClearShare:        r.Float(assistant.KeyClearShare),
		ClearAtLeastShare: r.Float(assistant.KeyClearAtLeastShare),
		CompactShare:      r.Float(assistant.KeyCompactShare),
		PreflightShare:    r.Float(assistant.KeyPreflightShare),
		ClearKeep:         r.Int(assistant.KeyClearKeep),
		PreserveMessages:  r.Int(assistant.KeyPreserveMessages),
		SummaryMaxTokens:  r.Int(assistant.KeySummaryMaxTokens),
		StubTokens:        r.Int(assistant.KeyStubTokens),
		PreviewChars:      r.Int(assistant.KeyPreviewChars),
		ManifestResults:   r.Int(assistant.KeyManifestResults),
	}
	if r.Err() != nil {
		return t, r.Err()
	}
	var err error
	t.Summary, err = prompts.Text(ctx, q, prompts.KeySummary)
	return t, err
}

func Ingest(ctx gocontext.Context, q db.Querier, runID, callID int64, tool string, value any, now float64, t Tuning) (string, *int64, error) {
	body := serialize(value)
	if EstimateTokens(body) <= int64(t.StubTokens) {
		return body, nil, nil
	}
	tokens := EstimateTokens(body)
	resultID, err := journal.StoreResult(ctx, q, runID, callID, tool, body, tokens, now)
	if err != nil {
		return "", nil, err
	}
	stub := map[string]any{
		"result_id": resultID,
		"tool":      tool,
		"tokens":    tokens,
		"preview":   textx.Clip(body, t.PreviewChars),
		"note":      StubNote,
	}
	return py.JSONDumps(stub, valueJSON), &resultID, nil
}

type Limits struct {
	Hard         int64
	Preflight    int64
	Compact      int64
	Clear        int64
	ClearAtLeast int64
	Tuning       Tuning
}

func LimitsFor(contextLimit int64, t Tuning) Limits {
	share := func(s float64) int64 { return int64(float64(contextLimit) * s) }
	return Limits{
		Hard:         contextLimit,
		Preflight:    share(t.PreflightShare),
		Compact:      share(t.CompactShare),
		Clear:        share(t.ClearShare),
		ClearAtLeast: share(t.ClearAtLeastShare),
		Tuning:       t,
	}
}

func typed(kind string, value int64) py.Dict {
	return py.Dict{{Key: "type", Value: kind}, {Key: "value", Value: value}}
}

func ClearsOf(m *anthropic.Message) (int64, int64) {
	if m == nil || m.ContextManagement == nil {
		return 0, 0
	}
	var count, cleared int64
	for _, e := range m.ContextManagement.AppliedEdits {
		if e.Type == ClearToolUses {
			count++
			cleared += e.ClearedInputTokens
		}
	}
	return count, cleared
}

func HistoryTokens(u anthropic.Usage) int64 {
	t := usage.TokensOf(u)
	for _, it := range u.Iterations {
		if it.Type == anthropic.IterationFallbackMessage {
			primary := usage.PrimaryTokens(u)
			return primary.Prompt() + it.OutputTokens
		}
	}
	return t.Input + t.CacheRead + t.CacheWrite5m + t.CacheWrite1h + t.Output
}

func decodeAll(content []json.RawMessage) ([]any, error) {
	out := make([]any, len(content))
	for i, b := range content {
		v, err := py.JSONLoads(b)
		if err != nil {
			return nil, fmt.Errorf("context: content block: %w", err)
		}
		out[i] = v
	}
	return out, nil
}

func encode(v any) json.RawMessage { return json.RawMessage(py.JSONDumps(v, compactJSON)) }

func str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

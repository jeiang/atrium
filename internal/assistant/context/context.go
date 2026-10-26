package context

import (
	gocontext "context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"unicode/utf8"

	"github.com/jeiang/atrium/internal/anthropic"
	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/assistant/prompts"
	"github.com/jeiang/atrium/internal/assistant/usage"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/py"
)

const (
	CharsPerToken = 4
	StubNote      = "Result stored; call recall(result_id, offset, limit) to read it in pages."
)

const (
	ModelContext          = 1_000_000
	SummaryEffort         = "low"
	BetaContextManagement = anthropic.BetaContextManagement
	BetaCompaction        = anthropic.BetaCompaction
	ClearToolUses         = "clear_tool_uses_20250919"
	Compact               = "compact_20260112"
	Compaction            = "compaction"
	Text                  = "text"
	ToolUse               = "tool_use"
	ToolResult            = "tool_result"
	Thinking              = "thinking"
	RedactedThinking      = "redacted_thinking"
	Attachment            = "attachment"
	recallTool            = "recall"
	inputTokens           = "input_tokens"
	compactionTruncate    = " [truncated for compaction]"
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
	KindServer = "server"
	KindClient = "client"
	KindCommit = "commit"
)

const SummarySystem = "You write faithful, source-cited summaries of atrium assistant sessions. Output only the summary."

func SummaryRequest(instructions string) string {
	return "The conversation above must be compacted. " + instructions + "\nWrite the summary now."
}

type ContextError string

func (e ContextError) Error() string { return string(e) }

func (ContextError) PyClass() string { return "ContextError" }

func Betas() []string { return []string{BetaContextManagement, BetaCompaction} }

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

func Serialize(value any) string {
	if s, ok := value.(string); ok {
		return s
	}
	return py.JSONDumps(value, valueJSON)
}

func Store(ctx gocontext.Context, q db.Querier, runID, callID int64, tool, body string, now float64) (int64, int64, error) {
	tokens := EstimateTokens(body)
	sum := sha256.Sum256([]byte(body))
	res, err := q.ExecContext(ctx,
		"INSERT INTO tool_results (run_id, tool_call_id, tool, tokens, bytes, sha256, body, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
		runID, callID, tool, tokens, len(body), hex.EncodeToString(sum[:]), body, now)
	if err != nil {
		return 0, 0, err
	}
	id, err := res.LastInsertId()
	return id, tokens, err
}

type Tuning struct {
	ClearShare        float64
	ClearAtLeastShare float64
	CompactShare      float64
	PreflightShare    float64
	ClearKeep         int
	CommitSavings     int
	PreserveMessages  int
	SummaryMaxTokens  int
	StubTokens        int
	PreviewChars      int
	DashboardItems    int
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
		CommitSavings:     r.Int(assistant.KeyCommitSavings),
		PreserveMessages:  r.Int(assistant.KeyPreserveMessages),
		SummaryMaxTokens:  r.Int(assistant.KeySummaryMaxTokens),
		StubTokens:        r.Int(assistant.KeyStubTokens),
		PreviewChars:      r.Int(assistant.KeyPreviewChars),
		DashboardItems:    r.Int(assistant.KeyDashboardItems),
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
	body := Serialize(value)
	if EstimateTokens(body) <= int64(t.StubTokens) {
		return body, nil, nil
	}
	resultID, tokens, err := Store(ctx, q, runID, callID, tool, body, now)
	if err != nil {
		return "", nil, err
	}
	stub := py.Dict{
		{Key: "result_id", Value: resultID},
		{Key: "tool", Value: tool},
		{Key: "tokens", Value: tokens},
		{Key: "preview", Value: py.Clip(body, t.PreviewChars)},
		{Key: "note", Value: StubNote},
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
	hard := min(contextLimit, ModelContext)
	share := func(s float64) int64 { return int64(float64(hard) * s) }
	return Limits{
		Hard:         hard,
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

func ContextManagement(l Limits) json.RawMessage {
	edits := []any{
		py.Dict{
			{Key: "type", Value: ClearToolUses},
			{Key: "trigger", Value: typed(inputTokens, l.Clear)},
			{Key: "keep", Value: typed("tool_uses", int64(l.Tuning.ClearKeep))},
			{Key: "clear_at_least", Value: typed(inputTokens, l.ClearAtLeast)},
			{Key: "clear_tool_inputs", Value: false},
			{Key: "exclude_tools", Value: []string{recallTool}},
		},
		py.Dict{
			{Key: "type", Value: Compact},
			{Key: "trigger", Value: typed(inputTokens, l.Compact)},
			{Key: "pause_after_compaction", Value: true},
			{Key: "instructions", Value: l.Tuning.Summary},
		},
	}
	return json.RawMessage(py.JSONDumps(py.Dict{{Key: "edits", Value: edits}}, compactJSON))
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
	return py.ReprValue(v)
}

// Package context is agent/context.py: what of a thread the model sees. Large tool results are stored and
// stubbed, every request carries the server-side context-management edits (tool-result clearing and
// compaction), the active message list is a projection of the journal (which is never rewritten) with the
// agent's pins, drops and folds applied, and compaction rebuilds that projection on the server's pause or on
// the client before a request would overflow.
//
// The package shares its name with the standard library's; importers alias it.
package context

import (
	gocontext "context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"unicode/utf8"

	"github.com/jeiang/atrium/internal/anthropic"
	"github.com/jeiang/atrium/internal/assistant/usage"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/py"
)

const (
	CharsPerToken = 4
	StubTokens    = 2000
	PreviewChars  = 600
	StubNote      = "Result stored; call recall(result_id, offset, limit) to read it in pages."
)

// Thresholds are shares of the thread's context limit (operator choice; ModelContext caps it): tool-result
// clearing from ClearShare, server compaction at CompactShare, client preflight compaction at PreflightShare,
// and nothing is sent over the limit itself.
const (
	ModelContext          = 1_000_000
	ClearShare            = 0.1875
	ClearKeep             = 3
	ClearAtLeastShare     = 0.05
	CompactShare          = 0.875
	PreflightShare        = 0.95
	CommitSavings         = 20_000
	PreserveMessages      = 3
	SummaryMaxTokens      = 8192
	SummaryEffort         = "low"
	DashboardItems        = 60
	ManifestResults       = 30
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

// Ledger item kinds and states (active is implicit: no ledger row), and projection kinds.
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

const CompactionInstructions = `Summarize this conversation so the assistant can continue the work exactly where it stopped. Keep every detail below; omit chit-chat and superseded reasoning.
1. The operator's goal and every constraint or preference they stated.
2. Open vs completed work: pending actions and their tool_use ids, subagent (research) ids and status; never mark an outstanding external action as done.
3. Drafts that were prepared but not sent (a draft is not a sent email), proposals awaiting approval, schedules created and their next due times.
4. Exact source identifiers, verbatim: mail message ids, Notion page ids, calendar event ids, Grafana dashboard/panel ids and queries, URLs.
5. Every result_id of stored tool results and what each contains, so recall(result_id, offset, limit) can page it back.
6. Pinned context item ids and the folds already made (with their source ids).
7. Facts learned, each with its source id; separate observed facts from inferences and note uncertainty.
8. The next step to take.`

const (
	SummarySystem  = "You write faithful, source-cited summaries of atrium assistant sessions. Output only the summary."
	SummaryRequest = "The conversation above must be compacted. " + CompactionInstructions + "\nWrite the summary now."
)

// ContextError is context.ContextError: a refused context operation, shown to the model as the tool's error.
type ContextError string

func (e ContextError) Error() string { return string(e) }

// Betas is list(BETAS): the beta flags every assistant request carries.
func Betas() []string { return []string{BetaContextManagement, BetaCompaction} }

var (
	compactJSON = py.JSONOpts{Compact: true, NoEnsureASCII: true}
	valueJSON   = py.JSONOpts{NoEnsureASCII: true}
)

// EstimateTokens is estimate_tokens: four code points to a token, rounded up.
func EstimateTokens(text string) int64 {
	return (int64(utf8.RuneCountInString(text)) + CharsPerToken - 1) / CharsPerToken
}

// EstimateValue is estimate_value: the token estimate of v (a decoded JSON value) as json.dumps writes it.
func EstimateValue(v any) int64 { return EstimateTokens(py.JSONDumps(v, valueJSON)) }

// EstimateContent is estimate_value of a message's content blocks.
func EstimateContent(content []json.RawMessage) (int64, error) {
	values, err := decodeAll(content)
	if err != nil {
		return 0, err
	}
	return EstimateValue(values), nil
}

// Serialize is serialize: a string as is, anything else (a value py.JSONDumps encodes) as JSON with ", " and
// ": " separators and non-ASCII kept.
func Serialize(value any) string {
	if s, ok := value.(string); ok {
		return s
	}
	return py.JSONDumps(value, valueJSON)
}

// Store is store: it keeps body in tool_results and returns its result_id and token estimate.
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

// Ingest is ingest: the tool_result content for a handler's value. Up to StubTokens it is the serialized
// value itself; above, the body is stored and the content is a stub naming its result_id, which is returned.
func Ingest(ctx gocontext.Context, q db.Querier, runID, callID int64, tool string, value any, now float64) (string, *int64, error) {
	body := Serialize(value)
	if EstimateTokens(body) <= StubTokens {
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
		{Key: "preview", Value: py.Clip(body, PreviewChars)},
		{Key: "note", Value: StubNote},
	}
	return py.JSONDumps(stub, valueJSON), &resultID, nil
}

// Limits is limits_for's result: token thresholds for one thread.
type Limits struct {
	Hard         int64
	Preflight    int64
	Compact      int64
	Clear        int64
	ClearAtLeast int64
}

// LimitsFor is limits_for: the thresholds as shares of the thread's context limit, capped at ModelContext.
func LimitsFor(contextLimit int64) Limits {
	hard := min(contextLimit, ModelContext)
	share := func(s float64) int64 { return int64(float64(hard) * s) }
	return Limits{
		Hard:         hard,
		Preflight:    share(PreflightShare),
		Compact:      share(CompactShare),
		Clear:        share(ClearShare),
		ClearAtLeast: share(ClearAtLeastShare),
	}
}

func typed(kind string, value int64) py.Dict {
	return py.Dict{{Key: "type", Value: kind}, {Key: "value", Value: value}}
}

// ContextManagement is context_management: the request's server-side edits, as the JSON value Python sends.
func ContextManagement(l Limits) json.RawMessage {
	edits := []any{
		py.Dict{
			{Key: "type", Value: ClearToolUses},
			{Key: "trigger", Value: typed(inputTokens, l.Clear)},
			{Key: "keep", Value: typed("tool_uses", ClearKeep)},
			{Key: "clear_at_least", Value: typed(inputTokens, l.ClearAtLeast)},
			{Key: "clear_tool_inputs", Value: false},
			{Key: "exclude_tools", Value: []string{recallTool}},
		},
		py.Dict{
			{Key: "type", Value: Compact},
			{Key: "trigger", Value: typed(inputTokens, l.Compact)},
			{Key: "pause_after_compaction", Value: true},
			{Key: "instructions", Value: CompactionInstructions},
		},
	}
	return json.RawMessage(py.JSONDumps(py.Dict{{Key: "edits", Value: edits}}, compactJSON))
}

// ClearsOf is clears_of: how many clear_tool_uses edits the server applied to the response's request and
// the input tokens they cleared.
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

// HistoryTokens is history_tokens: the size of the conversation after a response (its prompt plus its
// output), from the response's top-level usage.
func HistoryTokens(u anthropic.Usage) int64 {
	t := usage.TokensOf(u)
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

// str is str(v) of a decoded JSON value.
func str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return py.ReprValue(v)
}

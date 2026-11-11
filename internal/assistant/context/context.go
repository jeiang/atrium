package context

import (
	gocontext "context"
	"encoding/json"
	"fmt"

	"github.com/jeiang/atrium/internal/anthropic"
	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/assistant/block"
	"github.com/jeiang/atrium/internal/assistant/journal"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/inference"
	"github.com/jeiang/atrium/internal/jsonx"
	"github.com/jeiang/atrium/internal/textx"
	"github.com/jeiang/atrium/internal/tokens"
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

const StubNote = "Result stored; call recall(result_id, offset, limit) to read it in pages."

const (
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

var (
	compactJSON = jsonx.Opts{Compact: true, NoEnsureASCII: true}
	valueJSON   = jsonx.Opts{NoEnsureASCII: true}
)

func serialize(value any) string {
	if s, ok := value.(string); ok {
		return s
	}
	return jsonx.Dumps(value, valueJSON)
}

type Tuning struct {
	StubTokens      int
	PreviewChars    int
	ManifestResults int
}

func ReadTuning(ctx gocontext.Context, q db.Querier) (Tuning, error) {
	r := assistant.Read(ctx, q)
	t := Tuning{
		StubTokens:      r.Int(assistant.KeyStubTokens),
		PreviewChars:    r.Int(assistant.KeyPreviewChars),
		ManifestResults: r.Int(assistant.KeyManifestResults),
	}
	return t, r.Err()
}

func Ingest(ctx gocontext.Context, q db.Querier, runID, callID int64, tool string, value any, now float64, t Tuning) (string, *int64, error) {
	body := serialize(value)
	if tool == recallTool || tokens.Estimate(body) <= int64(t.StubTokens) {
		return body, nil, nil
	}
	n := tokens.Estimate(body)
	resultID, err := journal.StoreResult(ctx, q, runID, callID, tool, body, n, now)
	if err != nil {
		return "", nil, err
	}
	stub := map[string]any{
		"result_id": resultID,
		"tool":      tool,
		"tokens":    n,
		"preview":   textx.Clip(body, t.PreviewChars),
		"note":      StubNote,
	}
	return jsonx.Dumps(stub, valueJSON), &resultID, nil
}

func HistoryTokens(u anthropic.Usage) int64 {
	t := inference.TokensOf(u)
	for _, it := range u.Iterations {
		if it.Type == anthropic.IterationFallbackMessage {
			primary := inference.PrimaryTokens(u)
			return primary.Prompt() + it.OutputTokens
		}
	}
	return t.Input + t.CacheRead + t.CacheWrite5m + t.CacheWrite1h + t.Output
}

func decodeAll(content []json.RawMessage) ([]any, error) {
	out := make([]any, len(content))
	for i, b := range content {
		v, err := jsonx.Loads(b)
		if err != nil {
			return nil, fmt.Errorf("context: content block: %w", err)
		}
		out[i] = v
	}
	return out, nil
}

func encode(v any) json.RawMessage { return json.RawMessage(jsonx.Dumps(v, compactJSON)) }

func str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

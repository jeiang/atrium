package inference

import (
	"context"
	"database/sql"
	"fmt"
	"math"
)

type ImportedEntry struct {
	ID              int64
	TS              float64
	RequestID       string
	Source          string
	Provider        string
	ThreadID        int64
	Kind            string
	JobName         string
	Model           string
	Effort          string
	Iterations      string
	InputTokens     int64
	CacheWrite5m    int64
	CacheWrite1h    int64
	CacheRead       int64
	OutputTokens    int64
	ReasoningTokens int64
	Searches        int64
	ToolCalls       int64
	WallMS          int64
	Tier            string
	CostUSD         float64
	Partial         bool
	Unpriced        bool
	ClearEdits      int64
	ClearedTokens   int64
	Compaction      int64
	Month           string
}

func ImportSettled(ctx context.Context, tx *sql.Tx, e ImportedEntry) error {
	if math.IsNaN(e.CostUSD) || math.IsInf(e.CostUSD, 0) || e.CostUSD < 0 {
		return fmt.Errorf("usage row %d has an unusable cost %v", e.ID, e.CostUSD)
	}
	iterations := e.Iterations
	if iterations == "" {
		iterations = "[]"
	}
	_, err := tx.ExecContext(ctx,
		"INSERT INTO usage (id, attempt_key, ts, month, expires_at, settled_at, state, budget_class, reservation_nusd, "+
			"accounted_nusd, estimated_prompt_tokens, max_output_tokens, max_searches, cache_ttl, request_id, source, provider, "+
			"thread_id, run_id, kind, job_name, model, effort, iterations, input_tokens, cache_write_5m, cache_write_1h, cache_read, "+
			"output_tokens, reasoning_tokens, searches, tool_calls, wall_ms, tier, cost_usd, partial, unpriced, clear_edits, "+
			"cleared_tokens, compaction, error) "+
			"VALUES (?, ?, ?, ?, ?, ?, 'settled', ?, 0, ?, 0, 0, 0, '5m', ?, ?, ?, ?, NULL, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)",
		e.ID, fmt.Sprintf("v2:usage:%d", e.ID), e.TS, e.Month, e.TS, e.TS, classFor(e.Source, e.Kind), nanoUSD(e.CostUSD),
		e.RequestID, e.Source, e.Provider, nullable(e.ThreadID), e.Kind, nullableText(e.JobName), e.Model, e.Effort, iterations,
		e.InputTokens, e.CacheWrite5m, e.CacheWrite1h, e.CacheRead, e.OutputTokens, e.ReasoningTokens, e.Searches, e.ToolCalls,
		e.WallMS, e.Tier, e.CostUSD, e.Partial, e.Unpriced, e.ClearEdits, e.ClearedTokens, e.Compaction)
	return err
}

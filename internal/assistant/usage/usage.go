package usage

import (
	"context"
	"database/sql"
	"time"

	"github.com/jeiang/atrium/internal/db"

	"github.com/jeiang/atrium/internal/anthropic"
	"github.com/jeiang/atrium/internal/assistant/pricing"
	"github.com/jeiang/atrium/internal/py"
)

type Entry struct {
	TS            float64
	ThreadID      int64
	RunID         int64
	Kind          string
	Model         string
	Effort        string
	ClearEdits    int64
	ClearedTokens int64
	Compaction    int64
}

func tokensOf(input, creation, read, output int64, split *anthropic.CacheCreation) pricing.Tokens {
	t := pricing.Tokens{Input: input, CacheWrite5m: creation, CacheRead: read, Output: output}
	if split != nil {
		t.CacheWrite5m, t.CacheWrite1h = split.Ephemeral5mInputTokens, split.Ephemeral1hInputTokens
	}
	return t
}

func TokensOf(u anthropic.Usage) pricing.Tokens {
	return tokensOf(u.InputTokens, u.CacheCreationInputTokens, u.CacheReadInputTokens, u.OutputTokens, u.CacheCreation)
}

func SearchesOf(u anthropic.Usage) int64 {
	if u.ServerToolUse == nil {
		return 0
	}
	return u.ServerToolUse.WebSearchRequests
}

func BilledEntries(u anthropic.Usage) []pricing.Tokens {
	if len(u.Iterations) == 0 {
		return []pricing.Tokens{TokensOf(u)}
	}
	out := make([]pricing.Tokens, len(u.Iterations))
	for i, it := range u.Iterations {
		out[i] = tokensOf(it.InputTokens, it.CacheCreationInputTokens, it.CacheReadInputTokens, it.OutputTokens, it.CacheCreation)
	}
	return out
}

func Record(ctx context.Context, q db.Querier, e Entry, u anthropic.Usage) (float64, error) {
	entries := BilledEntries(u)
	var sum pricing.Tokens
	costs := make([]float64, len(entries))
	high := false
	flat := false
	for i, t := range entries {
		sum.Input += t.Input
		sum.CacheWrite5m += t.CacheWrite5m
		sum.CacheWrite1h += t.CacheWrite1h
		sum.CacheRead += t.CacheRead
		sum.Output += t.Output
		cost, err := pricing.CostUSD(e.Model, t, 0)
		if err != nil {
			return 0, err
		}
		costs[i] = cost
		tier, err := pricing.TierOf(e.Model, t)
		if err != nil {
			return 0, err
		}
		high = high || tier == pricing.TierHigh
		flat = tier == pricing.TierFlat
	}
	searches := SearchesOf(u)
	cost := py.Sum(costs) + float64(float64(searches)*pricing.SearchUSD)
	tier := pricing.TierLow
	if high {
		tier = pricing.TierHigh
	} else if flat {
		tier = pricing.TierFlat
	}
	_, err := q.ExecContext(ctx,
		"INSERT INTO usage (ts, thread_id, run_id, kind, model, effort, input_tokens, cache_write_5m, cache_write_1h, "+
			"cache_read, output_tokens, searches, tier, cost_usd, clear_edits, cleared_tokens, compaction) "+
			"VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		e.TS, e.ThreadID, e.RunID, e.Kind, e.Model, e.Effort, sum.Input, sum.CacheWrite5m, sum.CacheWrite1h,
		sum.CacheRead, sum.Output, searches, tier, cost, e.ClearEdits, e.ClearedTokens, e.Compaction)
	if err != nil {
		return 0, err
	}
	return cost, nil
}

func MonthStart(now float64, loc *time.Location) float64 {
	sec, us := py.SplitTimestamp(now)
	year, month, _ := time.Unix(sec, us*1000).In(loc).Date()
	wall := time.Date(year, month, 1, 0, 0, 0, 0, time.UTC).Unix()
	return float64(py.WallToUnix(loc, wall))
}

func MonthToDate(ctx context.Context, q db.Querier, now float64, loc *time.Location) (float64, error) {
	var cost float64
	err := q.QueryRowContext(ctx, "SELECT COALESCE(SUM(cost_usd), 0) AS cost FROM usage WHERE ts >= ?", MonthStart(now, loc)).Scan(&cost)
	return cost, err
}

func RunCost(ctx context.Context, q db.Querier, runID int64) (float64, error) {
	var cost float64
	err := q.QueryRowContext(ctx, "SELECT COALESCE(SUM(cost_usd), 0) AS cost FROM usage WHERE run_id = ?", runID).Scan(&cost)
	return cost, err
}

type Summary struct {
	Calls          int64   `json:"calls"`
	Cost           float64 `json:"cost"`
	InputTokens    int64   `json:"input_tokens"`
	CacheWrite     int64   `json:"cache_write"`
	CacheRead      int64   `json:"cache_read"`
	OutputTokens   int64   `json:"output_tokens"`
	Searches       int64   `json:"searches"`
	HighTier       int64   `json:"high_tier"`
	CacheReadShare float64 `json:"cache_read_share"`
}

const summarySQL = "SELECT COUNT(*) AS calls, COALESCE(SUM(cost_usd), 0) AS cost, COALESCE(SUM(input_tokens), 0) AS input_tokens, " +
	"COALESCE(SUM(cache_write_5m + cache_write_1h), 0) AS cache_write, COALESCE(SUM(cache_read), 0) AS cache_read, " +
	"COALESCE(SUM(output_tokens), 0) AS output_tokens, COALESCE(SUM(searches), 0) AS searches, " +
	"COALESCE(SUM(tier = ?), 0) AS high_tier FROM usage WHERE ts >= ?"

func MonthSummary(ctx context.Context, q db.Querier, now float64, loc *time.Location) (Summary, error) {
	var s Summary
	err := q.QueryRowContext(ctx, summarySQL, pricing.TierHigh, MonthStart(now, loc)).Scan(
		&s.Calls, &s.Cost, &s.InputTokens, &s.CacheWrite, &s.CacheRead, &s.OutputTokens, &s.Searches, &s.HighTier)
	if err != nil {
		return Summary{}, err
	}
	if prompt := s.InputTokens + s.CacheWrite + s.CacheRead; prompt != 0 {
		s.CacheReadShare = float64(s.CacheRead) / float64(prompt)
	}
	return s, nil
}

type KindRow struct {
	Kind     string  `json:"kind"`
	Calls    int64   `json:"calls"`
	Cost     float64 `json:"cost"`
	Searches int64   `json:"searches"`
}

func ByKind(ctx context.Context, q db.Querier, now float64, loc *time.Location) ([]KindRow, error) {
	rows, err := q.QueryContext(ctx,
		"SELECT kind, COUNT(*) AS calls, SUM(cost_usd) AS cost, SUM(searches) AS searches FROM usage "+
			"WHERE ts >= ? GROUP BY kind ORDER BY cost DESC", MonthStart(now, loc))
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (KindRow, error) {
		var k KindRow
		return k, r.Scan(&k.Kind, &k.Calls, &k.Cost, &k.Searches)
	})
}

type ThreadRow struct {
	ThreadID        int64   `json:"thread_id"`
	Title           *string `json:"title"`
	Kind            string  `json:"kind"`
	Calls           int64   `json:"calls"`
	Cost            float64 `json:"cost"`
	PromptTokens    int64   `json:"prompt_tokens"`
	MaxPromptTokens int64   `json:"max_prompt_tokens"`
	OutputTokens    int64   `json:"output_tokens"`
	Searches        int64   `json:"searches"`
}

func ByThread(ctx context.Context, q db.Querier, now float64, loc *time.Location, limit int) ([]ThreadRow, error) {
	rows, err := q.QueryContext(ctx,
		"SELECT u.thread_id, t.title, t.kind, COUNT(*) AS calls, SUM(u.cost_usd) AS cost, "+
			"SUM(u.input_tokens + u.cache_write_5m + u.cache_write_1h + u.cache_read) AS prompt_tokens, "+
			"MAX(u.input_tokens + u.cache_write_5m + u.cache_write_1h + u.cache_read) AS max_prompt_tokens, "+
			"SUM(u.output_tokens) AS output_tokens, SUM(u.searches) AS searches "+
			"FROM usage u JOIN threads t ON t.id = u.thread_id WHERE u.ts >= ? "+
			"GROUP BY u.thread_id ORDER BY cost DESC LIMIT ?", MonthStart(now, loc), limit)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (ThreadRow, error) {
		var t ThreadRow
		return t, r.Scan(&t.ThreadID, &t.Title, &t.Kind, &t.Calls, &t.Cost, &t.PromptTokens, &t.MaxPromptTokens, &t.OutputTokens, &t.Searches)
	})
}

const DefaultThreadLimit = 50

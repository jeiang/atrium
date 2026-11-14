package inference

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/anthropic"
	"github.com/jeiang/atrium/internal/assistant/pricing"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/numx"
	"github.com/jeiang/atrium/internal/settings"
	"github.com/jeiang/atrium/internal/timex"
)

const (
	KindMemory = "memory"

	SourceLoop         = "loop"
	SourceGateway      = "gateway"
	SourceSubscription = "claude_subscription"
	SourcePi           = "pi"

	ProviderAnthropic = "anthropic"

	NanoUSD = 1_000_000_000
)

type Iteration struct {
	Type         string  `json:"type"`
	Model        string  `json:"model"`
	Input        int64   `json:"input_tokens"`
	CacheWrite5m int64   `json:"cache_write_5m"`
	CacheWrite1h int64   `json:"cache_write_1h"`
	CacheRead    int64   `json:"cache_read"`
	Output       int64   `json:"output_tokens"`
	Tier         string  `json:"tier"`
	CostUSD      float64 `json:"cost_usd"`
	Unpriced     bool    `json:"unpriced,omitempty"`
}

type priced struct {
	iterations []Iteration
	tokens     pricing.Tokens
	searches   int64
	reasoning  int64
	tier       string
	cost       float64
	unpriced   bool
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

func ReasoningOf(u anthropic.Usage) int64 {
	if u.OutputTokensDetails == nil {
		return 0
	}
	return u.OutputTokensDetails.ThinkingTokens
}

func iterationsOf(model string, u anthropic.Usage) []Iteration {
	if len(u.Iterations) == 0 {
		t := TokensOf(u)
		cost, tier, unpriced := pricing.CostOrMax(model, t)
		return []Iteration{{
			Type: "message", Model: model, Input: t.Input, CacheWrite5m: t.CacheWrite5m, CacheWrite1h: t.CacheWrite1h,
			CacheRead: t.CacheRead, Output: t.Output, Tier: tier, CostUSD: cost, Unpriced: unpriced,
		}}
	}
	out := make([]Iteration, len(u.Iterations))
	for i, it := range u.Iterations {
		served := model
		if it.Type == anthropic.IterationFallbackMessage && it.Model != "" {
			served = it.Model
		}
		t := tokensOf(it.InputTokens, it.CacheCreationInputTokens, it.CacheReadInputTokens, it.OutputTokens, it.CacheCreation)
		cost, tier, unpriced := pricing.CostOrMax(served, t)
		out[i] = Iteration{
			Type: it.Type, Model: served, Input: t.Input, CacheWrite5m: t.CacheWrite5m, CacheWrite1h: t.CacheWrite1h,
			CacheRead: t.CacheRead, Output: t.Output, Tier: tier, CostUSD: cost, Unpriced: unpriced,
		}
	}
	return out
}

func priceUsage(model string, u anthropic.Usage) priced {
	iterations := iterationsOf(model, u)
	var sum pricing.Tokens
	var costs []float64
	high, flat, unpriced := false, true, false
	for _, it := range iterations {
		sum.Input += it.Input
		sum.CacheWrite5m += it.CacheWrite5m
		sum.CacheWrite1h += it.CacheWrite1h
		sum.CacheRead += it.CacheRead
		sum.Output += it.Output
		costs = append(costs, it.CostUSD)
		high = high || it.Tier == pricing.TierHigh
		flat = flat && it.Tier == pricing.TierFlat
		unpriced = unpriced || it.Unpriced
	}
	tier := pricing.TierLow
	if high {
		tier = pricing.TierHigh
	} else if flat {
		tier = pricing.TierFlat
	}
	searches := SearchesOf(u)
	return priced{
		iterations: iterations,
		tokens:     sum,
		searches:   searches,
		reasoning:  ReasoningOf(u),
		tier:       tier,
		cost:       numx.Sum(costs) + float64(float64(searches)*pricing.SearchUSD),
		unpriced:   unpriced,
	}
}

func nanoUSD(usd float64) int64 { return int64(math.Ceil(usd * NanoUSD)) }

func usdOf(n int64) float64 { return float64(n) / NanoUSD }

func classFor(source, kind string) Class {
	if source == SourceSubscription {
		return ClassSubscription
	}
	switch kind {
	case KindMemory, "cron", "nightly", "worker":
		return Unattended
	default:
		return Interactive
	}
}

func nullable(id int64) *int64 {
	if id == 0 {
		return nil
	}
	return &id
}

func nullableText(text string) *string {
	if text == "" {
		return nil
	}
	return &text
}

func Location(ctx context.Context, q db.Querier) (*time.Location, error) {
	found, err := settings.From(ctx).Resolve(ctx, q, config.KeyTZ)
	if err != nil {
		return nil, err
	}
	return config.Zone(found.Value)
}

func MonthKey(now float64, loc *time.Location) string {
	sec, us := timex.SplitTimestamp(now)
	return time.Unix(sec, us*1000).In(loc).Format("2006-01")
}

func MonthStart(now float64, loc *time.Location) float64 {
	sec, us := timex.SplitTimestamp(now)
	year, month, _ := time.Unix(sec, us*1000).In(loc).Date()
	wall := time.Date(year, month, 1, 0, 0, 0, 0, time.UTC).Unix()
	return float64(timex.WallToUnix(loc, wall))
}

func NextMonthStart(now float64, loc *time.Location) float64 {
	sec, us := timex.SplitTimestamp(now)
	year, month, _ := time.Unix(sec, us*1000).In(loc).Date()
	wall := time.Date(year, month+1, 1, 0, 0, 0, 0, time.UTC).Unix()
	return float64(timex.WallToUnix(loc, wall))
}

const committedSQL = "CASE WHEN budget_class = 'subscription' THEN 0 " +
	"WHEN state IN ('reserved','unknown') THEN reservation_nusd ELSE accounted_nusd END"

func MonthToDate(ctx context.Context, q db.Querier, now float64, loc *time.Location) (float64, error) {
	var n int64
	err := q.QueryRowContext(ctx, "SELECT COALESCE(SUM("+committedSQL+"), 0) FROM usage WHERE month = ? AND budget_class <> ?",
		MonthKey(now, loc), ClassSubscription).Scan(&n)
	return usdOf(n), err
}

func ThreadMonthToDate(ctx context.Context, q db.Querier, threadID int64, now float64, loc *time.Location) (float64, error) {
	var cost float64
	err := q.QueryRowContext(ctx, "SELECT COALESCE(SUM(cost_usd), 0) AS cost FROM usage WHERE thread_id = ? AND month = ?",
		threadID, MonthKey(now, loc)).Scan(&cost)
	return cost, err
}

func PromptTokens(ctx context.Context, q db.Querier, threadID int64) (int64, error) {
	var tokens int64
	err := q.QueryRowContext(ctx,
		"SELECT input_tokens + cache_write_5m + cache_write_1h + cache_read FROM usage WHERE thread_id = ? ORDER BY id DESC LIMIT 1",
		threadID).Scan(&tokens)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return tokens, err
}

func RunCost(ctx context.Context, q db.Querier, runID int64) (float64, error) {
	var cost float64
	err := q.QueryRowContext(ctx, "SELECT COALESCE(SUM(cost_usd), 0) AS cost FROM usage WHERE run_id = ?", runID).Scan(&cost)
	return cost, err
}

func SpendOnRuns(ctx context.Context, q db.Querier, runIDs []int64) (float64, error) {
	if len(runIDs) == 0 {
		return 0, nil
	}
	marks := strings.TrimSuffix(strings.Repeat("?,", len(runIDs)), ",")
	args := make([]any, len(runIDs))
	for i, id := range runIDs {
		args[i] = id
	}
	var cost float64
	err := q.QueryRowContext(ctx, "SELECT COALESCE(SUM(cost_usd), 0) FROM usage WHERE run_id IN ("+marks+")", args...).Scan(&cost)
	return cost, err
}

func CommittedOnRuns(ctx context.Context, q db.Querier, runIDs []int64) (int64, error) {
	if len(runIDs) == 0 {
		return 0, nil
	}
	marks := strings.TrimSuffix(strings.Repeat("?,", len(runIDs)), ",")
	args := make([]any, len(runIDs))
	for i, id := range runIDs {
		args[i] = id
	}
	var n int64
	err := q.QueryRowContext(ctx, "SELECT COALESCE(SUM("+committedSQL+"), 0) FROM usage WHERE run_id IN ("+marks+")", args...).Scan(&n)
	return n, err
}

func RunTokens(ctx context.Context, q db.Querier, runID int64) (int64, error) {
	var n int64
	err := q.QueryRowContext(ctx, "SELECT COALESCE(SUM(input_tokens + cache_write_5m + cache_write_1h + cache_read + output_tokens), 0) AS n "+
		"FROM usage WHERE run_id = ?", runID).Scan(&n)
	return n, err
}

type RunTotals struct {
	Input      int64
	Output     int64
	CacheRead  int64
	CacheWrite int64
	Cost       float64
}

func TotalsOfRun(ctx context.Context, q db.Querier, runID int64) (RunTotals, error) {
	var t RunTotals
	err := q.QueryRowContext(ctx,
		"SELECT COALESCE(SUM(input_tokens),0), COALESCE(SUM(output_tokens),0), COALESCE(SUM(cache_read),0), "+
			"COALESCE(SUM(cache_write_5m + cache_write_1h),0), COALESCE(SUM(cost_usd),0) FROM usage WHERE run_id = ?", runID,
	).Scan(&t.Input, &t.Output, &t.CacheRead, &t.CacheWrite, &t.Cost)
	return t, err
}

func SpendOnThreads(ctx context.Context, q db.Querier, threadIDs []int64, since float64) (float64, error) {
	if len(threadIDs) == 0 {
		return 0, nil
	}
	marks := strings.TrimSuffix(strings.Repeat("?,", len(threadIDs)), ",")
	args := make([]any, 0, len(threadIDs)+1)
	for _, id := range threadIDs {
		args = append(args, id)
	}
	args = append(args, since)
	var cost float64
	err := q.QueryRowContext(ctx, "SELECT COALESCE(SUM(cost_usd), 0) FROM usage WHERE thread_id IN ("+marks+") AND ts >= ?", args...).Scan(&cost)
	return cost, err
}

func ThreadCosts(ctx context.Context, q db.Querier, threadIDs []int64) (map[int64]float64, error) {
	out := make(map[int64]float64, len(threadIDs))
	if len(threadIDs) == 0 {
		return out, nil
	}
	marks := strings.TrimSuffix(strings.Repeat("?,", len(threadIDs)), ",")
	args := make([]any, len(threadIDs))
	for i, id := range threadIDs {
		args[i] = id
	}
	rows, err := q.QueryContext(ctx, "SELECT thread_id, COALESCE(SUM(cost_usd), 0) FROM usage WHERE thread_id IN ("+marks+") GROUP BY thread_id", args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id int64
		var cost float64
		if err := rows.Scan(&id, &cost); err != nil {
			return nil, err
		}
		out[id] = cost
	}
	return out, rows.Err()
}

func ThreadWarmth(ctx context.Context, q db.Querier, threadID int64) (float64, int64, bool, error) {
	var ts float64
	var prompt int64
	err := q.QueryRowContext(ctx,
		"SELECT ts, input_tokens + cache_write_5m + cache_write_1h + cache_read FROM usage WHERE thread_id = ? ORDER BY ts DESC LIMIT 1",
		threadID).Scan(&ts, &prompt)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0, false, nil
	}
	if err != nil {
		return 0, 0, false, err
	}
	return ts, prompt, true, nil
}

type Summary struct {
	Calls           int64   `json:"calls"`
	Cost            float64 `json:"cost"`
	InputTokens     int64   `json:"input_tokens"`
	CacheWrite      int64   `json:"cache_write"`
	CacheRead       int64   `json:"cache_read"`
	OutputTokens    int64   `json:"output_tokens"`
	ReasoningTokens int64   `json:"reasoning_tokens"`
	Searches        int64   `json:"searches"`
	ToolCalls       int64   `json:"tool_calls"`
	WallMS          int64   `json:"wall_ms"`
	HighTier        int64   `json:"high_tier"`
	Partial         int64   `json:"partial"`
	Unpriced        int64   `json:"unpriced"`
	CacheReadShare  float64 `json:"cache_read_share"`
}

const summaryFields = "COUNT(*) AS calls, COALESCE(SUM(cost_usd), 0) AS cost, COALESCE(SUM(input_tokens), 0) AS input_tokens, " +
	"COALESCE(SUM(cache_write_5m + cache_write_1h), 0) AS cache_write, COALESCE(SUM(cache_read), 0) AS cache_read, " +
	"COALESCE(SUM(output_tokens), 0) AS output_tokens, COALESCE(SUM(reasoning_tokens), 0) AS reasoning_tokens, " +
	"COALESCE(SUM(searches), 0) AS searches, COALESCE(SUM(tool_calls), 0) AS tool_calls, COALESCE(SUM(wall_ms), 0) AS wall_ms, " +
	"COALESCE(SUM(tier = ?), 0) AS high_tier, COALESCE(SUM(partial), 0) AS partial, COALESCE(SUM(unpriced), 0) AS unpriced"

func scanSummary(row *sql.Row) (Summary, error) {
	var s Summary
	err := row.Scan(&s.Calls, &s.Cost, &s.InputTokens, &s.CacheWrite, &s.CacheRead, &s.OutputTokens, &s.ReasoningTokens,
		&s.Searches, &s.ToolCalls, &s.WallMS, &s.HighTier, &s.Partial, &s.Unpriced)
	if err != nil {
		return Summary{}, err
	}
	if prompt := s.InputTokens + s.CacheWrite + s.CacheRead; prompt != 0 {
		s.CacheReadShare = float64(s.CacheRead) / float64(prompt)
	}
	return s, nil
}

func MonthSummary(ctx context.Context, q db.Querier, now float64, loc *time.Location) (Summary, error) {
	return scanSummary(q.QueryRowContext(ctx, "SELECT "+summaryFields+" FROM usage WHERE month = ? AND budget_class <> ?",
		pricing.TierHigh, MonthKey(now, loc), ClassSubscription))
}

func MemorySummary(ctx context.Context, q db.Querier, now float64, loc *time.Location) (Summary, error) {
	return scanSummary(q.QueryRowContext(ctx, "SELECT "+summaryFields+" FROM usage WHERE month = ? AND kind = ?",
		pricing.TierHigh, MonthKey(now, loc), KindMemory))
}

func SubscriptionSummary(ctx context.Context, q db.Querier, now float64, loc *time.Location) (Summary, error) {
	return scanSummary(q.QueryRowContext(ctx, "SELECT "+summaryFields+" FROM usage WHERE month = ? AND budget_class = ?",
		pricing.TierHigh, MonthKey(now, loc), ClassSubscription))
}

type KindRow struct {
	Kind      string  `json:"kind"`
	Calls     int64   `json:"calls"`
	Cost      float64 `json:"cost"`
	Searches  int64   `json:"searches"`
	ToolCalls int64   `json:"tool_calls"`
	WallMS    int64   `json:"wall_ms"`
}

func ByKind(ctx context.Context, q db.Querier, now float64, loc *time.Location) ([]KindRow, error) {
	rows, err := q.QueryContext(ctx,
		"SELECT kind, COUNT(*) AS calls, SUM(cost_usd) AS cost, SUM(searches) AS searches, SUM(tool_calls) AS tool_calls, "+
			"SUM(wall_ms) AS wall_ms FROM usage WHERE month = ? AND budget_class <> ? GROUP BY kind ORDER BY cost DESC",
		MonthKey(now, loc), ClassSubscription)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (KindRow, error) {
		var k KindRow
		return k, r.Scan(&k.Kind, &k.Calls, &k.Cost, &k.Searches, &k.ToolCalls, &k.WallMS)
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
	ReasoningTokens int64   `json:"reasoning_tokens"`
	Searches        int64   `json:"searches"`
}

func ByThread(ctx context.Context, q db.Querier, now float64, loc *time.Location, limit int) ([]ThreadRow, error) {
	rows, err := q.QueryContext(ctx,
		"SELECT thread_id, COUNT(*) AS calls, SUM(cost_usd) AS cost, "+
			"SUM(input_tokens + cache_write_5m + cache_write_1h + cache_read) AS prompt_tokens, "+
			"MAX(input_tokens + cache_write_5m + cache_write_1h + cache_read) AS max_prompt_tokens, "+
			"SUM(output_tokens) AS output_tokens, SUM(reasoning_tokens) AS reasoning_tokens, SUM(searches) AS searches "+
			"FROM usage WHERE month = ? AND budget_class <> ? AND thread_id IS NOT NULL "+
			"GROUP BY thread_id ORDER BY cost DESC LIMIT ?", MonthKey(now, loc), ClassSubscription, limit)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (ThreadRow, error) {
		var t ThreadRow
		return t, r.Scan(&t.ThreadID, &t.Calls, &t.Cost, &t.PromptTokens, &t.MaxPromptTokens, &t.OutputTokens, &t.ReasoningTokens, &t.Searches)
	})
}

const DefaultThreadLimit = 50

func PrimaryTokens(u anthropic.Usage) pricing.Tokens {
	for _, it := range u.Iterations {
		if it.Type != anthropic.IterationFallbackMessage {
			return tokensOf(it.InputTokens, it.CacheCreationInputTokens, it.CacheReadInputTokens, it.OutputTokens, it.CacheCreation)
		}
	}
	return TokensOf(u)
}

func HasRows(ctx context.Context, q db.Querier) (bool, error) {
	var one int
	err := q.QueryRowContext(ctx, "SELECT 1 FROM usage LIMIT 1").Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

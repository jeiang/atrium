package usage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/jeiang/atrium/internal/anthropic"
	"github.com/jeiang/atrium/internal/assistant/pricing"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/numx"
	"github.com/jeiang/atrium/internal/obs"
	"github.com/jeiang/atrium/internal/timex"
)

const (
	KindMemory = "memory"

	SourceLoop         = "loop"
	SourceGateway      = "gateway"
	SourceSubscription = "claude_subscription"
	SourcePi           = "pi"

	ProviderAnthropic = "anthropic"
)

type MissingRequestID struct{}

func (MissingRequestID) Error() string { return "usage: the request has no id" }

type Entry struct {
	TS         float64
	RequestID  string
	Source     string
	Provider   string
	ThreadID   int64
	RunID      int64
	Kind       string
	Model      string
	Effort     string
	ToolCalls  int64
	WallMS     int64
	Partial    bool
	Compaction int64
}

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

func Record(ctx context.Context, d *db.DB, e Entry, u anthropic.Usage) (float64, error) {
	if e.RequestID == "" {
		return 0, MissingRequestID{}
	}
	if e.Source == "" {
		e.Source = SourceLoop
	}
	if e.Provider == "" {
		e.Provider = ProviderAnthropic
	}
	iterations := iterationsOf(e.Model, u)
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
	cost := numx.Sum(costs) + float64(float64(searches)*pricing.SearchUSD)
	raw, err := json.Marshal(iterations)
	if err != nil {
		return 0, err
	}
	reasoning := ReasoningOf(u)
	_, err = d.Write.ExecContext(ctx,
		"INSERT INTO usage (ts, request_id, source, provider, thread_id, run_id, kind, job_name, model, effort, iterations, "+
			"input_tokens, cache_write_5m, cache_write_1h, cache_read, output_tokens, reasoning_tokens, searches, tool_calls, wall_ms, "+
			"tier, cost_usd, partial, unpriced, compaction) "+
			"VALUES (?, ?, ?, ?, ?, ?, ?, (SELECT j.name FROM threads t JOIN jobs j ON j.id = t.job_id WHERE t.id = ?), ?, ?, ?, "+
			"?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(request_id) DO NOTHING",
		e.TS, e.RequestID, e.Source, e.Provider, nullable(e.ThreadID), nullable(e.RunID), e.Kind, e.ThreadID, e.Model, e.Effort,
		string(raw), sum.Input, sum.CacheWrite5m, sum.CacheWrite1h, sum.CacheRead, sum.Output, reasoning,
		searches, e.ToolCalls, e.WallMS, tier, cost, e.Partial, unpriced, e.Compaction)
	if err != nil {
		return 0, err
	}
	kind := e.Kind
	if kind == "" {
		kind = "unknown"
	}
	model := e.Model
	if model == "" {
		model = "unknown"
	}
	obs.LLMRequests.Add(1, model, kind, strconv.FormatBool(e.Partial))
	obs.LLMDuration.Observe(float64(e.WallMS)/1000, model, kind)
	obs.LLMTokens.Add(float64(sum.Input), model, kind, "input")
	obs.LLMTokens.Add(float64(sum.CacheWrite5m+sum.CacheWrite1h), model, kind, "cache_write")
	obs.LLMTokens.Add(float64(sum.CacheRead), model, kind, "cache_read")
	obs.LLMTokens.Add(float64(sum.Output), model, kind, "output")
	obs.LLMTokens.Add(float64(reasoning), model, kind, "reasoning")
	obs.LLMCost.Add(cost, model, kind)
	return cost, nil
}

func nullable(id int64) *int64 {
	if id == 0 {
		return nil
	}
	return &id
}

func MonthStart(now float64, loc *time.Location) float64 {
	sec, us := timex.SplitTimestamp(now)
	year, month, _ := time.Unix(sec, us*1000).In(loc).Date()
	wall := time.Date(year, month, 1, 0, 0, 0, 0, time.UTC).Unix()
	return float64(timex.WallToUnix(loc, wall))
}

const budgetSQL = "kind <> ? AND source <> ?"

func MonthToDate(ctx context.Context, q db.Querier, now float64, loc *time.Location) (float64, error) {
	var cost float64
	err := q.QueryRowContext(ctx, "SELECT COALESCE(SUM(cost_usd), 0) AS cost FROM usage WHERE ts >= ? AND "+budgetSQL,
		MonthStart(now, loc), KindMemory, SourceSubscription).Scan(&cost)
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
	return scanSummary(q.QueryRowContext(ctx, "SELECT "+summaryFields+" FROM usage WHERE ts >= ? AND "+budgetSQL,
		pricing.TierHigh, MonthStart(now, loc), KindMemory, SourceSubscription))
}

func MemorySummary(ctx context.Context, q db.Querier, now float64, loc *time.Location) (Summary, error) {
	return scanSummary(q.QueryRowContext(ctx, "SELECT "+summaryFields+" FROM usage WHERE ts >= ? AND kind = ?",
		pricing.TierHigh, MonthStart(now, loc), KindMemory))
}

func SubscriptionSummary(ctx context.Context, q db.Querier, now float64, loc *time.Location) (Summary, error) {
	return scanSummary(q.QueryRowContext(ctx, "SELECT "+summaryFields+" FROM usage WHERE ts >= ? AND source = ?",
		pricing.TierHigh, MonthStart(now, loc), SourceSubscription))
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
			"SUM(wall_ms) AS wall_ms FROM usage WHERE ts >= ? AND source <> ? GROUP BY kind ORDER BY cost DESC",
		MonthStart(now, loc), SourceSubscription)
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
		"SELECT u.thread_id, t.title, t.kind, COUNT(*) AS calls, SUM(u.cost_usd) AS cost, "+
			"SUM(u.input_tokens + u.cache_write_5m + u.cache_write_1h + u.cache_read) AS prompt_tokens, "+
			"MAX(u.input_tokens + u.cache_write_5m + u.cache_write_1h + u.cache_read) AS max_prompt_tokens, "+
			"SUM(u.output_tokens) AS output_tokens, SUM(u.reasoning_tokens) AS reasoning_tokens, SUM(u.searches) AS searches "+
			"FROM usage u JOIN threads t ON t.id = u.thread_id WHERE u.ts >= ? AND u.source <> ? "+
			"GROUP BY u.thread_id ORDER BY cost DESC LIMIT ?", MonthStart(now, loc), SourceSubscription, limit)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (ThreadRow, error) {
		var t ThreadRow
		return t, r.Scan(&t.ThreadID, &t.Title, &t.Kind, &t.Calls, &t.Cost, &t.PromptTokens, &t.MaxPromptTokens, &t.OutputTokens, &t.ReasoningTokens, &t.Searches)
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

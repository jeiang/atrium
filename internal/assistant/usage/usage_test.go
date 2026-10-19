package usage_test

import (
	"context"
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/anthropic"
	"github.com/jeiang/atrium/internal/assistant/pricing"
	"github.com/jeiang/atrium/internal/assistant/usage"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/db/dbtest"
	"github.com/jeiang/atrium/internal/testutil/golden"
)

func mustLoc(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func seed(t *testing.T, d *db.DB, threadID, runID int64) {
	t.Helper()
	ctx := t.Context()
	if _, err := d.Write.ExecContext(ctx, "INSERT OR IGNORE INTO threads (id, kind, effort, created_at, updated_at) VALUES (?, 'chat', 'medium', 0, 0)", threadID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.ExecContext(ctx, "INSERT OR IGNORE INTO runs (id, thread_id, kind, model, effort, state, started_at) VALUES (?, ?, 'chat', 'm', 'medium', 'done', 0)", runID, threadID); err != nil {
		t.Fatal(err)
	}
}

type row struct {
	TS            float64 `json:"ts"`
	ThreadID      int64   `json:"thread_id"`
	RunID         int64   `json:"run_id"`
	Kind          string  `json:"kind"`
	Model         string  `json:"model"`
	Effort        string  `json:"effort"`
	Input         int64   `json:"input_tokens"`
	CacheWrite5m  int64   `json:"cache_write_5m"`
	CacheWrite1h  int64   `json:"cache_write_1h"`
	CacheRead     int64   `json:"cache_read"`
	Output        int64   `json:"output_tokens"`
	Searches      int64   `json:"searches"`
	Tier          string  `json:"tier"`
	Cost          float64 `json:"cost_usd"`
	ClearEdits    int64   `json:"clear_edits"`
	ClearedTokens int64   `json:"cleared_tokens"`
	Compaction    int64   `json:"compaction"`
}

func TestRecordPricesEveryIterationLikeTheGolden(t *testing.T) {
	scenario := golden.LoadScenario(t, "anthropic", "usage_iterations")
	d := dbtest.Schema(t)
	var wants []row
	for _, raw := range scenario.Usage {
		var r row
		if err := json.Unmarshal(raw, &r); err != nil {
			t.Fatal(err)
		}
		wants = append(wants, r)
	}
	if len(wants) != len(scenario.Exchanges) {
		t.Fatalf("%d usage rows for %d exchanges", len(wants), len(scenario.Exchanges))
	}
	for i, ex := range scenario.Exchanges {
		var resp struct {
			Usage anthropic.Usage `json:"usage"`
		}
		if err := json.Unmarshal(ex.Response, &resp); err != nil {
			t.Fatal(err)
		}
		w := wants[i]
		seed(t, d, w.ThreadID, w.RunID)
		cost, err := usage.Record(t.Context(), d.Write, usage.Entry{
			TS: w.TS, ThreadID: w.ThreadID, RunID: w.RunID, Kind: w.Kind, Model: w.Model, Effort: w.Effort,
			ClearEdits: w.ClearEdits, ClearedTokens: w.ClearedTokens, Compaction: w.Compaction,
		}, resp.Usage)
		if err != nil {
			t.Fatal(err)
		}
		if cost != w.Cost {
			t.Errorf("exchange %d: cost %v, golden %v", i, cost, w.Cost)
		}
		var got row
		err = d.Read.QueryRowContext(t.Context(),
			"SELECT ts, thread_id, run_id, kind, model, effort, input_tokens, cache_write_5m, cache_write_1h, cache_read, output_tokens, "+
				"searches, tier, cost_usd, clear_edits, cleared_tokens, compaction FROM usage ORDER BY id DESC LIMIT 1").Scan(
			&got.TS, &got.ThreadID, &got.RunID, &got.Kind, &got.Model, &got.Effort, &got.Input, &got.CacheWrite5m, &got.CacheWrite1h,
			&got.CacheRead, &got.Output, &got.Searches, &got.Tier, &got.Cost, &got.ClearEdits, &got.ClearedTokens, &got.Compaction)
		if err != nil {
			t.Fatal(err)
		}
		if got != w {
			t.Errorf("exchange %d: row %+v, golden %+v", i, got, w)
		}
	}
	if wants[0].Tier != pricing.TierHigh || wants[0].Input != 302000 {
		t.Fatalf("the golden no longer shows the summed high-tier compaction row: %+v", wants[0])
	}
}

func TestEveryGoldenScenarioPricesLikePython(t *testing.T) {
	manifest := golden.LoadManifest(t, "anthropic")
	checked := 0
	for _, entry := range manifest.Scenarios {
		scenario := golden.LoadScenario(t, "anthropic", entry.Name)
		if len(scenario.Usage) == 0 || len(scenario.Usage) != len(scenario.Exchanges) {
			continue
		}
		t.Run(entry.Name, func(t *testing.T) {
			d := dbtest.Schema(t)
			for i, ex := range scenario.Exchanges {
				var want row
				if err := json.Unmarshal(scenario.Usage[i], &want); err != nil {
					t.Fatal(err)
				}
				var resp struct {
					Usage anthropic.Usage `json:"usage"`
				}
				if err := json.Unmarshal(ex.Response, &resp); err != nil {
					t.Fatal(err)
				}
				seed(t, d, want.ThreadID, want.RunID)
				cost, err := usage.Record(t.Context(), d.Write, usage.Entry{TS: want.TS, ThreadID: want.ThreadID, RunID: want.RunID,
					Kind: want.Kind, Model: want.Model, Effort: want.Effort}, resp.Usage)
				if err != nil {
					t.Fatal(err)
				}
				if cost != want.Cost {
					t.Errorf("response %d: cost %v, golden %v", i, cost, want.Cost)
				}
			}
			checked++
		})
	}
	if checked == 0 {
		t.Fatal("no scenario had one usage row per exchange")
	}
}

func TestTokensOfSplitsCacheWrites(t *testing.T) {
	u := anthropic.Usage{InputTokens: 10, CacheCreationInputTokens: 30, CacheCreation: &anthropic.CacheCreation{Ephemeral1hInputTokens: 10, Ephemeral5mInputTokens: 20},
		CacheReadInputTokens: 5, OutputTokens: 7}
	if got, want := usage.TokensOf(u), (pricing.Tokens{Input: 10, CacheWrite5m: 20, CacheWrite1h: 10, CacheRead: 5, Output: 7}); got != want {
		t.Errorf("TokensOf = %+v, want %+v", got, want)
	}
	flat := anthropic.Usage{CacheCreationInputTokens: 30}
	if got := usage.TokensOf(flat); got.CacheWrite5m != 30 || got.CacheWrite1h != 0 {
		t.Errorf("without a split the writes bill as 5m: %+v", got)
	}
}

func TestRecordSearchesAndLedger(t *testing.T) {
	d := dbtest.Schema(t)
	seed(t, d, 1, 1)
	u := anthropic.Usage{InputTokens: 10, CacheCreationInputTokens: 30, CacheCreation: &anthropic.CacheCreation{Ephemeral1hInputTokens: 10, Ephemeral5mInputTokens: 20},
		CacheReadInputTokens: 5, OutputTokens: 7, ServerToolUse: &anthropic.ServerToolUse{WebSearchRequests: 3}}
	cost, err := usage.Record(t.Context(), d.Write, usage.Entry{TS: 1792080000, ThreadID: 1, RunID: 1, Kind: "chat", Model: pricing.Model, Effort: "medium"}, u)
	if err != nil {
		t.Fatal(err)
	}
	want := pricing.CostUSD(pricing.Tokens{Input: 10, CacheWrite5m: 20, CacheWrite1h: 10, CacheRead: 5, Output: 7}, 3)
	if math.Abs(cost-want) > 1e-12 {
		t.Errorf("cost %v, want about %v", cost, want)
	}
	loc := mustLoc(t, "America/Port_of_Spain")
	s, err := usage.MonthSummary(t.Context(), d.Read, 1792080100, loc)
	if err != nil {
		t.Fatal(err)
	}
	if s.Calls != 1 || s.Searches != 3 || s.HighTier != 0 || s.InputTokens != 10 || s.CacheWrite != 30 || s.CacheRead != 5 || s.OutputTokens != 7 {
		t.Errorf("summary %+v", s)
	}
	if share := 5.0 / 45.0; s.CacheReadShare != share {
		t.Errorf("cache read share %v, want %v", s.CacheReadShare, share)
	}
	kinds, err := usage.ByKind(t.Context(), d.Read, 1792080100, loc)
	if err != nil || len(kinds) != 1 || kinds[0].Kind != "chat" || kinds[0].Searches != 3 {
		t.Errorf("ByKind = %+v, %v", kinds, err)
	}
	threads, err := usage.ByThread(t.Context(), d.Read, 1792080100, loc, usage.DefaultThreadLimit)
	if err != nil || len(threads) != 1 || threads[0].ThreadID != 1 || threads[0].PromptTokens != 45 || threads[0].MaxPromptTokens != 45 {
		t.Errorf("ByThread = %+v, %v", threads, err)
	}
	if rc, err := usage.RunCost(t.Context(), d.Read, 1); err != nil || rc != cost {
		t.Errorf("RunCost = %v, %v", rc, err)
	}
	if rc, err := usage.RunCost(t.Context(), d.Read, 99); err != nil || rc != 0 {
		t.Errorf("RunCost of no run = %v, %v", rc, err)
	}
}

func TestMonthStartFollowsTheZone(t *testing.T) {
	spain := mustLoc(t, "America/Port_of_Spain")
	tokyo := mustLoc(t, "Asia/Tokyo")
	moment := float64(time.Date(2026, 10, 31, 20, 0, 0, 0, spain).Unix())
	if got, want := usage.MonthStart(moment, spain), float64(time.Date(2026, 10, 1, 0, 0, 0, 0, spain).Unix()); got != want {
		t.Errorf("Port of Spain month start %v, want %v", got, want)
	}
	if got, want := usage.MonthStart(moment, tokyo), float64(time.Date(2026, 11, 1, 0, 0, 0, 0, tokyo).Unix()); got != want {
		t.Errorf("Tokyo month start %v, want %v", got, want)
	}
	edge := float64(time.Date(2026, 11, 1, 0, 0, 0, 0, spain).Unix())
	if usage.MonthStart(edge-1, spain) == usage.MonthStart(edge, spain) {
		t.Error("the instant before midnight on the 1st is still the previous month")
	}
	if got := usage.MonthStart(edge, spain); got != edge {
		t.Errorf("midnight on the 1st starts its own month: %v, want %v", got, edge)
	}
}

func TestMonthToDateIgnoresEarlierMonths(t *testing.T) {
	d := dbtest.Schema(t)
	seed(t, d, 1, 1)
	loc := mustLoc(t, "Asia/Tokyo")
	now := float64(time.Date(2026, 11, 1, 9, 0, 0, 0, loc).Unix())
	for _, c := range []struct {
		ts   float64
		cost float64
	}{
		{float64(time.Date(2026, 10, 31, 23, 59, 0, 0, loc).Unix()), 5},
		{float64(time.Date(2026, 11, 1, 0, 0, 0, 0, loc).Unix()), 2},
		{now, 3},
	} {
		if _, err := d.Write.ExecContext(t.Context(),
			"INSERT INTO usage (ts, thread_id, run_id, kind, model, effort, input_tokens, cache_write_5m, cache_write_1h, cache_read, output_tokens, searches, tier, cost_usd) "+
				"VALUES (?, 1, 1, 'chat', 'm', 'medium', 0, 0, 0, 0, 0, 0, '<=100K', ?)", c.ts, c.cost); err != nil {
			t.Fatal(err)
		}
	}
	got, err := usage.MonthToDate(t.Context(), d.Read, now, loc)
	if err != nil || got != 5 {
		t.Errorf("MonthToDate = %v, %v; want 5", got, err)
	}
	if got, err := usage.MonthToDate(context.Background(), d.Read, now, mustLoc(t, "America/Port_of_Spain")); err != nil || got != 10 {
		t.Errorf("in Port of Spain it is still October: %v, %v; want 10", got, err)
	}
}

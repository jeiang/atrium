package pages

import (
	"encoding/json"
	"math"
	"net/http"
	"testing"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/assistant/journal"
	"github.com/jeiang/atrium/internal/assistant/pricing"
	"github.com/jeiang/atrium/internal/assistant/usage"
	"github.com/jeiang/atrium/internal/settings"
)

func TestUsageExcludesMemoryFromMonthlySpend(t *testing.T) {
	settings.Default.SetPins(nil)
	s := newSite(t)
	s.exec("INSERT INTO settings (key, value) VALUES ('monthly_budget_usd', '2')")
	s.exec("INSERT INTO threads (id, kind, effort, created_at, updated_at) VALUES (1, 'chat', 'low', 0, 0)")
	s.exec("INSERT INTO runs (id, thread_id, kind, model, effort, state, started_at) VALUES (1, 1, 'chat', 'm', 'low', 'done', 0)")
	insert := "INSERT INTO usage (ts, request_id, source, provider, thread_id, run_id, kind, model, effort, iterations, input_tokens, cache_write_5m, cache_write_1h, cache_read, output_tokens, searches, tier, cost_usd) " +
		"VALUES (?, lower(hex(randomblob(16))), 'loop', 'anthropic', 1, 1, ?, 'm', 'low', '[]', 0, 0, 0, 0, 0, 0, 'low', ?)"
	s.exec(insert, s.now(), "chat", 1.5)
	s.exec(insert, s.now(), "memory", 100.0)
	page := status(t, s.get("/settings/usage"), http.StatusOK)
	mustContain(t, page, "$1.50", "1 memory summaries", "$100.00 excluded from the budget", "75% of $2 used", "$100.000")
}

func TestUsageLedgerRecordsOneRowPerRequest(t *testing.T) {
	s := newSite(t)
	u := anthropictest.Usage{Input: 1000, Output: 100, Reasoning: 40, Searches: 2, Iterations: []anthropictest.Iteration{
		{Type: "message", Input: 600, Output: 10},
		{Type: "fallback_message", Model: pricing.OpusID, Input: 400, Output: 90},
	}}
	s.api.Push(reply1(anthropictest.Fallback(pricing.HaikuID, pricing.OpusID, "cyber"), anthropictest.Text("served")).WithUsage(u))
	thread := s.startThread("hi")
	if got := s.settle(s.lastRun(thread)); got.State != journal.Done {
		t.Fatalf("run %s %v", got.State, got.Error)
	}
	var requestID, source, provider, kind, model, iterations string
	var input, output, reasoning, searches, tools, wall, partial, unpriced int64
	var cost float64
	if err := s.db.Read.QueryRow("SELECT request_id, source, provider, kind, model, iterations, input_tokens, output_tokens, "+
		"reasoning_tokens, searches, tool_calls, wall_ms, partial, unpriced, cost_usd FROM usage").Scan(
		&requestID, &source, &provider, &kind, &model, &iterations, &input, &output, &reasoning, &searches, &tools, &wall,
		&partial, &unpriced, &cost); err != nil {
		t.Fatal(err)
	}
	if requestID == "" || source != usage.SourceLoop || provider != usage.ProviderAnthropic || kind != "chat" || model != pricing.HaikuID {
		t.Errorf("row %s %s %s %s %s", requestID, source, provider, kind, model)
	}
	if input != 1000 || output != 100 || reasoning != 40 || searches != 2 || tools != 0 || partial != 0 || unpriced != 0 {
		t.Errorf("counts %d %d %d %d %d %d %d", input, output, reasoning, searches, tools, partial, unpriced)
	}
	if wall < 0 {
		t.Errorf("wall time %d", wall)
	}
	var iters []usage.Iteration
	if err := json.Unmarshal([]byte(iterations), &iters); err != nil {
		t.Fatal(err)
	}
	if len(iters) != 2 || iters[0].Model != pricing.HaikuID || iters[1].Model != pricing.OpusID {
		t.Fatalf("iterations %v", iters)
	}
	wantHaiku, err := pricing.CostUSD(pricing.HaikuID, pricing.Tokens{Input: 600, Output: 10}, 0)
	if err != nil {
		t.Fatal(err)
	}
	wantOpus, err := pricing.CostUSD(pricing.OpusID, pricing.Tokens{Input: 400, Output: 90}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if iters[0].CostUSD != wantHaiku || iters[1].CostUSD != wantOpus {
		t.Errorf("iteration costs %v, want %v and %v", iters, wantHaiku, wantOpus)
	}
	if want := wantHaiku + wantOpus + 2*pricing.SearchUSD; math.Abs(cost-want) > 1e-12 {
		t.Errorf("row cost %v, want %v", cost, want)
	}
	page := status(t, s.get("/settings/usage"), http.StatusOK)
	mustContain(t, page, "1 API calls", "0 tool calls", "40 reasoning", "2 web searches")
}

func TestUsageLedgerRecordsAPartialRowForAnAbortedStream(t *testing.T) {
	s := newSite(t)
	s.api.Push(anthropictest.StreamFailure("api_error", "connection dropped").WithUsage(anthropictest.Usage{Input: 500, Output: 20}))
	thread := s.startThread("hi")
	if got := s.settle(s.lastRun(thread)); got.State != journal.Failed {
		t.Fatalf("run %s %v, want a failed run", got.State, got.Error)
	}
	var requestID string
	var input, output, partial int64
	var cost float64
	if err := s.db.Read.QueryRow("SELECT request_id, input_tokens, output_tokens, partial, cost_usd FROM usage").
		Scan(&requestID, &input, &output, &partial, &cost); err != nil {
		t.Fatal(err)
	}
	if requestID == "" || input != 500 || output != 20 || partial != 1 || cost <= 0 {
		t.Errorf("partial row %s %d %d %d %v", requestID, input, output, partial, cost)
	}
	mustContain(t, status(t, s.get("/settings/usage"), http.StatusOK), "1 partial")
}

func TestUsageLedgerFlagsAnUnknownModelAsUnpriced(t *testing.T) {
	s := newSite(t)
	u := anthropictest.Usage{Input: 1000, Output: 100, Iterations: []anthropictest.Iteration{
		{Type: "message", Input: 600, Output: 10},
		{Type: "fallback_message", Model: "claude-mystery-9", Input: 400, Output: 90},
	}}
	s.api.Push(reply1(anthropictest.Fallback(pricing.HaikuID, "claude-mystery-9", "cyber"), anthropictest.Text("served")).WithUsage(u))
	thread := s.startThread("hi")
	if got := s.settle(s.lastRun(thread)); got.State != journal.Done {
		t.Fatalf("run %s %v", got.State, got.Error)
	}
	var iterations string
	var unpriced int64
	var cost float64
	if err := s.db.Read.QueryRow("SELECT iterations, unpriced, cost_usd FROM usage").Scan(&iterations, &unpriced, &cost); err != nil {
		t.Fatal(err)
	}
	var iters []usage.Iteration
	if err := json.Unmarshal([]byte(iterations), &iters); err != nil {
		t.Fatal(err)
	}
	if unpriced != 1 || len(iters) != 2 || !iters[1].Unpriced || iters[1].CostUSD <= 0 || cost <= 0 {
		t.Errorf("unpriced row %d %v %v", unpriced, iters, cost)
	}
	page := status(t, s.get("/settings/usage"), http.StatusOK)
	mustContain(t, page, "1 unpriced")
}

func TestSubscriptionRowsStayOutOfTheMonthlyBudget(t *testing.T) {
	s := newSite(t)
	s.exec("INSERT INTO settings (key, value) VALUES ('monthly_budget_usd', '10')")
	s.exec("INSERT INTO threads (id, kind, effort, created_at, updated_at) VALUES (1, 'chat', 'low', 0, 0)")
	s.exec("INSERT INTO runs (id, thread_id, kind, model, effort, state, started_at) VALUES (1, 1, 'chat', 'm', 'low', 'done', 0)")
	insert := "INSERT INTO usage (ts, request_id, source, provider, thread_id, run_id, kind, model, effort, iterations, input_tokens, cache_write_5m, cache_write_1h, cache_read, output_tokens, searches, tier, cost_usd) " +
		"VALUES (?, lower(hex(randomblob(16))), ?, 'anthropic', 1, 1, 'chat', 'm', 'low', '[]', 0, 0, 0, 0, 0, 0, 'low', ?)"
	s.exec(insert, s.now(), usage.SourceLoop, 1.5)
	s.exec(insert, s.now(), usage.SourceSubscription, 5.0)
	page := status(t, s.get("/settings/usage"), http.StatusOK)
	mustContain(t, page, "$1.50", "15% of $10 used", "1 subscription requests · $5.00 uncapped")
	mustNotContain(t, page, "$6.50")
}

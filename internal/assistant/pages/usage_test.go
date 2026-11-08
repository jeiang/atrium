package pages

import (
	"net/http"
	"testing"
)

func TestUsageExcludesMemoryFromMonthlySpend(t *testing.T) {
	s := newSite(t)
	s.exec("INSERT INTO settings (key, value) VALUES ('monthly_budget_usd', '2')")
	s.exec("INSERT INTO threads (id, kind, effort, created_at, updated_at) VALUES (1, 'chat', 'low', 0, 0)")
	s.exec("INSERT INTO runs (id, thread_id, kind, model, effort, state, started_at) VALUES (1, 1, 'chat', 'm', 'low', 'done', 0)")
	insert := "INSERT INTO usage (ts, thread_id, run_id, kind, model, effort, input_tokens, cache_write_5m, cache_write_1h, cache_read, output_tokens, searches, tier, cost_usd) " +
		"VALUES (?, 1, 1, ?, 'm', 'low', 0, 0, 0, 0, 0, 0, 'low', ?)"
	s.exec(insert, s.now(), "chat", 1.5)
	s.exec(insert, s.now(), "memory", 100.0)
	page := status(t, s.get("/settings/usage"), http.StatusOK)
	mustContain(t, page, "$1.50", "1 memory summaries", "$100.00 excluded from the budget", "75% of $2 used", "$100.000")
}

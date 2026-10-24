package pages

import (
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/assistant/journal"
)

func (s *site) store(key, value string) {
	s.t.Helper()
	s.exec("INSERT OR REPLACE INTO settings (key, value) VALUES (?, ?)", key, value)
}

func (s *site) opusRun() *journal.Run {
	s.t.Helper()
	r := s.multipart("/chat", map[string]string{"text": "hi", "model": "claude-opus-5-5", "context_limit": "1000000"}, nil, hx, "true")
	status(s.t, r, http.StatusNoContent)
	id, err := strconv.ParseInt(strings.TrimPrefix(r.Header.Get("HX-Redirect"), "/chat/"), 10, 64)
	if err != nil {
		s.t.Fatal(err)
	}
	return s.settle(s.lastRun(id))
}

var coldRequest = regexp.MustCompile(`about (\d+) tokens`)

func TestRunCostCapIsTTLAwareAndRefusesBeforeSending(t *testing.T) {
	s := newSite(t)
	s.store("max_run_cost_usd_opus_5_5", "0.0001")
	refused := s.opusRun()
	if refused.State != journal.Failed || refused.Error == nil || !strings.Contains(*refused.Error, "run cost cap") {
		t.Fatalf("run %s %v, want a run cost cap stop", refused.State, refused.Error)
	}
	if n := len(s.api.Requests()); n != 0 {
		t.Fatalf("%d requests sent, want the cap to refuse before the first", n)
	}
	found := coldRequest.FindStringSubmatch(*refused.Error)
	if found == nil {
		t.Fatalf("no token count in %q", *refused.Error)
	}
	tokens, err := strconv.ParseFloat(found[1], 64)
	if err != nil {
		t.Fatal(err)
	}
	fiveMinutes, oneHour := tokens*5/1e6, tokens*8/1e6
	s.store("max_run_cost_usd_opus_5_5", strconv.FormatFloat((fiveMinutes+oneHour)/2, 'f', -1, 64))

	s.store("cache_ttl_chat", "5m")
	s.api.Push(reply1(anthropictest.Text("sent")))
	if ran := s.opusRun(); ran.State != journal.Done {
		t.Fatalf("5m run %s %v, want it under the cap", ran.State, ran.Error)
	}
	if n := len(s.api.Requests()); n != 1 {
		t.Fatalf("%d requests after the 5m run, want 1", n)
	}

	s.store("cache_ttl_chat", "1h")
	held := s.opusRun()
	if held.State != journal.Failed || held.Error == nil || !strings.Contains(*held.Error, "run cost cap") {
		t.Fatalf("1h run %s %v, want the dearer 1h write to pass the cap", held.State, held.Error)
	}
	if n := len(s.api.Requests()); n != 1 {
		t.Fatalf("%d requests after the 1h refusal, want still 1", n)
	}
}

func TestSpendCap429EndsTheRunAsABudgetStop(t *testing.T) {
	s := newSite(t)
	s.api.Push(anthropictest.Failure(http.StatusTooManyRequests, "rate_limit_error", "You have reached your specified API usage limits."))
	thread := s.startThread("hi")
	got := s.settle(s.lastRun(thread))
	if got.State != journal.Failed || got.Error == nil || !strings.Contains(*got.Error, "Anthropic spend limit reached") {
		t.Fatalf("run %s %v, want a budget stop", got.State, got.Error)
	}
	if n := len(s.api.Requests()); n != 1 {
		t.Errorf("%d requests, want the 429 not retried", n)
	}

	limited := anthropictest.Failure(http.StatusTooManyRequests, "rate_limit_error", "slow down")
	limited.Header = http.Header{"Retry-After": {"30"}}
	s.api.Push(limited)
	other := s.startThread("again")
	got = s.settle(s.lastRun(other))
	if got.State != journal.Failed || got.Error == nil || strings.Contains(*got.Error, "spend limit") {
		t.Fatalf("rate limited run %s %v, want an ordinary failure", got.State, got.Error)
	}
}

package main

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/anthropic"
	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
)

func TestInferenceTransportRecordsAFullResponse(t *testing.T) {
	t.Parallel()
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	asstWarm(t)
	d := asstDB(t, dir)

	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("done")).
		WithUsage(anthropictest.Usage{Input: 1000, CacheRead: 200, Output: 100}))
	v, _ := asstRun(t, "--text", "go")
	if v.State != "done" {
		t.Fatalf("full response run: %+v %q", v, v.err())
	}
	var requestID string
	var input, cacheRead, output, partial int64
	var cost float64
	if err := d.Read.QueryRow("SELECT request_id, input_tokens, cache_read, output_tokens, partial, cost_usd FROM usage WHERE run_id = ?", v.ID).
		Scan(&requestID, &input, &cacheRead, &output, &partial, &cost); err != nil {
		t.Fatal(err)
	}
	if requestID == "" || input != 1000 || cacheRead != 200 || output != 100 || partial != 0 || cost <= 0 {
		t.Errorf("full row %q %d %d %d %d %v", requestID, input, cacheRead, output, partial, cost)
	}
	if n := asstCount(t, d, "SELECT COUNT(*) FROM usage WHERE run_id = ?", v.ID); n != 1 {
		t.Errorf("%d usage rows for the run, want 1", n)
	}
}

func TestInferenceTransportRecordsKnownUsageWhenTheStreamBreaksAfterStart(t *testing.T) {
	t.Parallel()
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	asstWarm(t)
	d := asstDB(t, dir)

	api.Push(anthropictest.Response{
		StopReason:          "end_turn",
		Usage:               anthropictest.Usage{Input: 500, CacheRead: 100, Output: 40}.JSON(),
		StartUsage:          anthropictest.Usage{Input: 500, CacheRead: 100}.JSON(),
		InterruptAfterStart: true,
	})
	v, _ := asstRun(t, "--text", "go")
	if v.State != "failed" || !strings.Contains(v.err(), anthropic.ConnectionMessage) {
		t.Fatalf("broken stream run: %+v %q", v, v.err())
	}
	var requestID string
	var input, cacheRead, output, partial int64
	var cost float64
	if err := d.Read.QueryRow("SELECT request_id, input_tokens, cache_read, output_tokens, partial, cost_usd FROM usage WHERE run_id = ?", v.ID).
		Scan(&requestID, &input, &cacheRead, &output, &partial, &cost); err != nil {
		t.Fatal(err)
	}
	if requestID == "" || input != 500 || cacheRead != 100 || output != 0 || partial != 1 || cost <= 0 {
		t.Errorf("partial row %q %d %d %d %d %v", requestID, input, cacheRead, output, partial, cost)
	}
}

func TestInferenceTransportRecordsNoUsageWhenTheProviderRejectsTheRequest(t *testing.T) {
	t.Parallel()
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	asstWarm(t)
	d := asstDB(t, dir)

	api.Push(anthropictest.Failure(http.StatusBadRequest, "invalid_request_error", "max_tokens: must be greater than 0"))
	sent := len(api.Requests())
	v, _ := asstRun(t, "--text", "go")
	if v.State != "failed" || !strings.Contains(v.err(), "max_tokens") {
		t.Fatalf("rejected run: %+v %q", v, v.err())
	}
	if n := asstCount(t, d, "SELECT COUNT(*) FROM usage WHERE run_id = ?", v.ID); n != 0 {
		t.Errorf("%d usage rows for the rejected run, want none", n)
	}
	if got := len(api.Requests()) - sent; got != 1 {
		t.Errorf("%d requests sent, want 1", got)
	}
}

func TestInferenceTransportRecordsNoUsageWhenTheConnectionNeverAnswers(t *testing.T) {
	t.Parallel()
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	asstWarm(t)
	d := asstDB(t, dir)

	api.Disconnect.Store(true)
	sent := len(api.Requests())
	v, _ := asstRun(t, "--text", "go")
	if v.State != "failed" || !strings.Contains(v.err(), anthropic.ConnectionMessage) {
		t.Fatalf("disconnected run: %+v %q", v, v.err())
	}
	if n := asstCount(t, d, "SELECT COUNT(*) FROM usage WHERE run_id = ?", v.ID); n != 0 {
		t.Errorf("%d usage rows for the disconnected run, want none", n)
	}
	if got := len(api.Requests()) - sent; got < 1 {
		t.Errorf("%d requests reached the server, want at least 1", got)
	}
}

func TestInferenceTransportRecordsKnownUsageWhenAStreamStallsAndTheCallerCancels(t *testing.T) {
	t.Parallel()
	dir, api, f := moduleFixture(t, flagsExcept("", ""))
	asstWarm(t)
	d := asstDB(t, dir)

	api.Push(anthropictest.Response{
		StopReason: "end_turn",
		Usage:      anthropictest.Usage{Input: 700, Output: 90}.JSON(),
		StartUsage: anthropictest.Usage{Input: 700}.JSON(),
		Content:    anthropictest.WebSearch("s1", "atrium"),
		Stall:      true,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan appRun, 1)
	go func() { done <- runAppContext(t, f, ctx, "agent", "run", "--text", "go") }()
	deadline := time.Now().Add(30 * time.Second)
	for asstCount(t, d, "SELECT COUNT(*) FROM run_events WHERE kind = 'tool_start'") == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the stalled stream never delivered its start")
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("the canceled run did not return")
	}

	var input, output, partial int64
	if err := d.Read.QueryRow("SELECT input_tokens, output_tokens, partial FROM usage ORDER BY id DESC LIMIT 1").
		Scan(&input, &output, &partial); err != nil {
		t.Fatal(err)
	}
	if input != 700 || output != 0 || partial != 1 {
		t.Errorf("stalled row %d %d %d", input, output, partial)
	}
}

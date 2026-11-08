package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/assistant/budget"
	"github.com/jeiang/atrium/internal/assistant/loop"
	"github.com/jeiang/atrium/internal/assistant/pricing"
	"github.com/jeiang/atrium/internal/db"
)

type runView struct {
	ThreadID int64   `json:"thread_id"`
	State    string  `json:"state"`
	Error    *string `json:"error"`
}

func agentRun(t *testing.T, args ...string) runView {
	t.Helper()
	got := runApp(t, append([]string{"agent", "run"}, args...)...)
	var v runView
	if err := json.Unmarshal([]byte(got.stdout), &v); err != nil {
		t.Fatalf("agent run: %d %q %q", got.code, got.stdout, got.stderr)
	}
	return v
}

var nextRequest = regexp.MustCompile(`about (\d+) tokens`)

func TestThreadCostCapCountsEveryRunOfTheThread(t *testing.T) {
	t.Parallel()
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	setEnv(t, settingEnv(loop.CacheTTLKey), "5m")
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("first")))
	first := agentRun(t, "--text", "hello")
	if first.State != "done" {
		t.Fatalf("first run: %+v", first)
	}
	thread := strconv.FormatInt(first.ThreadID, 10)

	setEnv(t, settingEnv(budget.ThreadCapKey), "0.000001")
	sent := len(api.Requests())
	refused := agentRun(t, "--thread", thread, "--text", "again")
	if refused.State != "failed" || refused.Error == nil || !strings.Contains(*refused.Error, "thread cost cap") || len(api.Requests()) != sent {
		t.Fatalf("run over the thread cap: %+v, %d requests sent", refused, len(api.Requests())-sent)
	}
	found := nextRequest.FindStringSubmatch(*refused.Error)
	if found == nil {
		t.Fatalf("no request size in %q", *refused.Error)
	}
	tokens, _ := strconv.ParseInt(found[1], 10, 64)
	cold, err := budget.ColdRequestUSD(pricing.HaikuID, tokens, "5m")
	if err != nil {
		t.Fatal(err)
	}
	threadSpend := func() float64 {
		t.Helper()
		d, err := db.Connect(filepath.Join(dir, "atrium.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = d.Close() }()
		var spent float64
		if err := d.Read.QueryRow("SELECT COALESCE(SUM(cost_usd), 0) FROM usage WHERE thread_id = ?", first.ThreadID).Scan(&spent); err != nil {
			t.Fatal(err)
		}
		return spent
	}
	spent := threadSpend()
	setEnv(t, settingEnv(budget.ThreadCapKey), fmt.Sprintf("%.8f", spent+cold*1.5))
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("second")))
	if ran := agentRun(t, "--thread", thread, "--text", "one"); ran.State != "done" {
		t.Fatalf("run under the thread cap: %+v", ran)
	}
	setEnv(t, settingEnv(budget.ThreadCapKey), fmt.Sprintf("%.8f", cold+threadSpend()/2))
	sent = len(api.Requests())
	if held := agentRun(t, "--thread", thread, "--text", "two"); held.State != "failed" || held.Error == nil ||
		!strings.Contains(*held.Error, budget.ThreadCapKey) || len(api.Requests()) != sent {
		t.Errorf("run once earlier runs of the thread used the cap: %+v", held)
	}
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("elsewhere")))
	if fresh := agentRun(t, "--text", "elsewhere"); fresh.State != "done" {
		t.Errorf("a new thread under the same cap: %+v", fresh)
	}
}

func TestComposerThreadCostCapRefusesTheNextRequest(t *testing.T) {
	dir, api, f := moduleFixture(t, flagsExcept("", ""))
	setEnv(t, settingEnv(assistant.KeyDefaultModel), pricing.HaikuID)
	warm(t, api, nil)
	base, _, stop := startServeApp(t, freshApp(f, nil), &lockedBuffer{})
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if status, _, _ := fetch(t, client, "GET", base+"/chat", nil); status != http.StatusOK {
		t.Fatalf("GET /chat = %d", status)
	}
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("costly")).WithUsage(anthropictest.Usage{Input: 1000, Output: 120_000}))
	if status, body, _ := fetch(t, client, "POST", base+"/chat", url.Values{"text": {"hello"}, "cost_cap": {"0.05"}}); status != http.StatusSeeOther {
		t.Fatalf("POST /chat = %d: %s", status, body)
	}
	thread := latestThread(t, dir)
	for deadline := time.Now().Add(20 * time.Second); !runDone(t, dir, thread); {
		if time.Now().After(deadline) {
			t.Fatal("first run did not finish")
		}
		time.Sleep(20 * time.Millisecond)
	}
	stop()
	sent := len(api.Requests())
	refused := agentRun(t, "--thread", fmt.Sprint(thread), "--text", "again")
	if len(api.Requests()) != sent {
		t.Fatalf("%d model requests past the thread's own cost cap, want 0", len(api.Requests())-sent)
	}
	if refused.State != "failed" || refused.Error == nil || !strings.Contains(*refused.Error, "thread cost cap") || !strings.Contains(*refused.Error, "of $0.05") {
		t.Errorf("run over the thread's own $0.05 cap: %+v", refused)
	}
}

func runDone(t *testing.T, dir string, thread int64) bool {
	t.Helper()
	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
	defer func() { _ = d.Close() }()
	var n int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM runs WHERE thread_id = ? AND state = 'done'", thread).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n > 0
}

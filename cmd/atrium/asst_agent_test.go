package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/assistant/pricing"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/inference"
	"github.com/jeiang/atrium/internal/mail/imapclient"
	"github.com/jeiang/atrium/internal/sdnotify/sdnotifytest"
	"github.com/jeiang/atrium/internal/settings"
)

type asstView struct {
	ID       int64   `json:"id"`
	ThreadID int64   `json:"thread_id"`
	State    string  `json:"state"`
	Error    *string `json:"error"`
	Cost     float64 `json:"cost"`
}

func (v asstView) err() string {
	if v.Error == nil {
		return ""
	}
	return *v.Error
}

func asstRun(t *testing.T, args ...string) (asstView, appRun) {
	t.Helper()
	ran := runApp(t, append([]string{"agent", "run"}, args...)...)
	var v asstView
	if err := json.Unmarshal([]byte(ran.stdout), &v); err != nil {
		t.Fatalf("agent run %v: exit %d stdout %q stderr %q", args, ran.code, ran.stdout, ran.stderr)
	}
	return v, ran
}

func asstWarm(t *testing.T) {
	t.Helper()
	if ran := runApp(t, "agent", "run", "--text", "warm"); ran.code != cli.ExitOK {
		t.Fatalf("warm run: %d %s", ran.code, ran.stderr)
	}
}

func asstSet(t *testing.T, key, value string) {
	t.Helper()
	if ran := runApp(t, "settings", "set", key, value); ran.code != cli.ExitOK {
		t.Fatalf("settings set %s %s: %d %s", key, value, ran.code, ran.stderr)
	}
}

func asstDB(t *testing.T, dir string) *db.DB {
	t.Helper()
	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func asstCount(t *testing.T, d *db.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := d.Read.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

func asstExec(t *testing.T, d *db.DB, query string, args ...any) {
	t.Helper()
	if _, err := d.Write.Exec(query, args...); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
}

func asstApp(f *fixture) *cli.App {
	reg := settings.New()
	return newAppWith(f.lookup, reg, config.NewState(reg), nil, imapclient.Opener, nil, quietLog)
}

func asstRunApp(app *cli.App, ctx context.Context, args ...string) appRun {
	var out, errs lockedBuffer
	code := app.Run(ctx, args, &out, &errs)
	return appRun{code, out.String(), errs.String()}
}

func asstReply(text string) anthropictest.Response {
	return anthropictest.Reply("end_turn", anthropictest.Text(text))
}

func asstToolResult(t *testing.T, d *db.DB, thread int64) string {
	t.Helper()
	var raw string
	if err := d.Read.QueryRow("SELECT content_json FROM thread_messages WHERE thread_id = ? AND role = 'user' AND content_json LIKE '%tool_result%' ORDER BY seq DESC LIMIT 1", thread).Scan(&raw); err != nil {
		t.Fatalf("tool result of thread %d: %v", thread, err)
	}
	return raw
}

func asstLastAssistant(t *testing.T, d *db.DB, thread int64) string {
	t.Helper()
	var raw string
	if err := d.Read.QueryRow("SELECT content_json FROM thread_messages WHERE thread_id = ? AND role = 'assistant' ORDER BY seq DESC LIMIT 1", thread).Scan(&raw); err != nil {
		t.Fatalf("assistant message of thread %d: %v", thread, err)
	}
	return raw
}

func asstSpend(t *testing.T, d *db.DB, source string, usd float64) {
	t.Helper()
	now := float64(time.Now().UnixMicro()) / 1e6
	loc, err := time.LoadLocation("America/Port_of_Spain")
	if err != nil {
		t.Fatal(err)
	}
	class := "interactive"
	if source == inference.SourceSubscription {
		class = "subscription"
	}
	asstExec(t, d, "INSERT INTO usage (attempt_key, ts, month_start, expires_at, settled_at, state, budget_class, reservation_nusd, accounted_nusd, "+
		"request_id, source, provider, thread_id, run_id, kind, model, effort, iterations, input_tokens, cache_write_5m, cache_write_1h, cache_read, "+
		"output_tokens, searches, tier, cost_usd) "+
		"VALUES (lower(hex(randomblob(16))), ?, ?, ?, ?, 'settled', ?, 0, ?, lower(hex(randomblob(16))), ?, 'anthropic', 1, 1, 'chat', "+
		"'claude-haiku-5-5', 'medium', '[]', 0, 0, 0, 0, 0, 0, '<=100K', ?)",
		now, inference.MonthStart(now, loc), now, now, class, int64(math.Ceil(usd*1e9)), source, usd)
}

func TestAsstMonthlyBudgetPausesUnattendedRunsAt80AndChatAt100(t *testing.T) {
	t.Parallel()
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	asstWarm(t)
	d := asstDB(t, dir)

	asstSpend(t, d, inference.SourceSubscription, 1000)
	api.Push(asstReply("cron under budget"))
	if ran := runApp(t, "agent", "run", "--text", "hello", "--kind", "cron"); ran.code != cli.ExitOK {
		t.Fatalf("cron run with only subscription spend: exit %d stderr %q", ran.code, ran.stderr)
	}

	asstSpend(t, d, inference.SourceLoop, 120)
	sent := len(api.Requests())
	ran := runApp(t, "agent", "run", "--text", "hello", "--kind", "cron")
	if ran.code != cli.ExitError || !strings.Contains(ran.stderr, "$150.00 spent (80%); unattended runs pause at 80%") || len(api.Requests()) != sent {
		t.Fatalf("cron run at 80%%: exit %d stderr %q, %d requests sent", ran.code, ran.stderr, len(api.Requests())-sent)
	}
	api.Push(asstReply("chat at 80"))
	if ran := runApp(t, "agent", "run", "--text", "hello"); ran.code != cli.ExitOK {
		t.Fatalf("chat run at 80%%: exit %d stderr %q", ran.code, ran.stderr)
	}

	asstSpend(t, d, inference.SourceLoop, 30)
	sent = len(api.Requests())
	ran = runApp(t, "agent", "run", "--text", "hello")
	if ran.code != cli.ExitError || !strings.Contains(ran.stderr, "(100%); chat pauses at 95%") || len(api.Requests()) != sent {
		t.Fatalf("chat run at 100%%: exit %d stderr %q, %d requests sent", ran.code, ran.stderr, len(api.Requests())-sent)
	}
	if n := api.Remaining(); n != 0 {
		t.Errorf("%d scripted replies unused", n)
	}
}

func asstProbes(batch string, n int, vary bool) []anthropictest.Response {
	out := make([]anthropictest.Response, n)
	for i := range out {
		key := "k"
		if vary {
			key = fmt.Sprintf("k%d", i)
		}
		out[i] = anthropictest.Reply("tool_use", anthropictest.ToolUse(fmt.Sprintf("%s%d", batch, i), "profile_get", map[string]any{"key": key}))
	}
	return out
}

func TestAsstRunsStopAtTheStepCapAndTheDoomLoopThreshold(t *testing.T) {
	t.Parallel()
	_, api, _ := moduleFixture(t, flagsExcept("", ""))
	asstWarm(t)

	asstSet(t, "max_steps_cron", "2")
	api.Push(asstProbes("a", 2, true)...)
	if v, _ := asstRun(t, "--text", "go", "--kind", "cron"); v.State != "failed" || v.err() != "stopped: step cap reached after 2 steps in this cron run (max_steps_cron)" {
		t.Errorf("cron run at a cap of 2: %+v %q", v, v.err())
	}
	asstSet(t, "max_steps_cron", "3")
	api.Push(asstProbes("b", 3, true)...)
	if v, _ := asstRun(t, "--text", "go", "--kind", "cron"); v.err() != "stopped: step cap reached after 3 steps in this cron run (max_steps_cron)" {
		t.Errorf("cron run at a cap of 3: %+v %q", v, v.err())
	}
	asstSet(t, "max_steps_chat", "5")
	api.Push(asstProbes("c", 1, true)...)
	api.Push(asstReply("ok"))
	if v, _ := asstRun(t, "--text", "go"); v.State != "done" {
		t.Errorf("a chat run was bound by the cron cap: %+v %q", v, v.err())
	}

	asstSet(t, "max_steps_cron", "10")
	api.Push(asstProbes("d", 3, false)...)
	if v, _ := asstRun(t, "--text", "go", "--kind", "cron"); v.State != "failed" || !strings.Contains(v.err(), "repeated 3 times with the same result") {
		t.Errorf("repeated call at the default threshold: %+v %q", v, v.err())
	}
	asstSet(t, "doom_loop_threshold", "2")
	api.Push(asstProbes("e", 2, false)...)
	if v, _ := asstRun(t, "--text", "go", "--kind", "cron"); !strings.Contains(v.err(), "repeated 2 times with the same result") {
		t.Errorf("repeated call at a threshold of 2: %+v %q", v, v.err())
	}
	if n := api.Remaining(); n != 0 {
		t.Errorf("%d scripted replies unused", n)
	}
}

func TestAsstRefusalFailsTheRunAndKeepsNothingTheModelProduced(t *testing.T) {
	t.Parallel()
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	asstWarm(t)
	d := asstDB(t, dir)

	api.Push(anthropictest.Refusal("cyber", "Exploit development is not supported.", anthropictest.Text("partial")))
	v, _ := asstRun(t, "--text", "go")
	if v.State != "failed" || v.err() != "the model refused the request (stop reason: refusal, category: cyber)" {
		t.Errorf("refused run: %+v %q", v, v.err())
	}
	text := asstLastAssistant(t, d, v.ThreadID)
	for _, want := range []string{"category: cyber", "Exploit development is not supported.", "Nothing it produced was kept"} {
		if !strings.Contains(text, want) {
			t.Errorf("thread message %s lacks %q", text, want)
		}
	}
	if strings.Contains(text, "partial") {
		t.Errorf("refused content was kept: %s", text)
	}
	if n := asstCount(t, d, "SELECT COUNT(*) FROM usage WHERE run_id = ? AND model = ?", v.ID, pricing.HaikuID); n != 1 {
		t.Errorf("%d ledger rows for the refused run, want 1", n)
	}

	refused := anthropictest.Refusal("general_harms", "")
	refused.StopDetails = json.RawMessage(`{"type":"refusal","category":null,"explanation":null,"recommended_model":"claude-haiku-5-5"}`)
	api.Push(refused)
	asstSet(t, "default_model", pricing.OpusID)
	v, _ = asstRun(t, "--text", "go")
	if text := asstLastAssistant(t, d, v.ThreadID); v.err() != "the model refused the request (stop reason: refusal)" || !strings.Contains(text, "retrying on claude-haiku-5-5") {
		t.Errorf("refusal without a fallback: %q, thread message %s", v.err(), text)
	}
}

func TestAsstToolChoiceFollowsTheCatalogAndRefusesAnUnavailableTool(t *testing.T) {
	t.Parallel()
	_, api, _ := moduleFixture(t, flagsExcept("", ""))
	asstWarm(t)

	sent := len(api.Requests())
	if v, _ := asstRun(t, "--text", "go", "--tool-choice", "missing"); v.State != "failed" || v.err() != `tool_choice names "missing", which is not available to this run` || len(api.Requests()) != sent {
		t.Errorf("unavailable tool_choice: %+v %q, %d requests sent", v, v.err(), len(api.Requests())-sent)
	}

	for _, tc := range []struct {
		model, force string
		want         string
	}{
		{pricing.HaikuID, "kb_get", `{"name":"kb_get","type":"tool"}`},
		{pricing.SonnetID, "any", ""},
	} {
		asstSet(t, "default_model", tc.model)
		api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("tc-"+tc.model, "kb_get", nil)), asstReply("done"))
		start := len(api.Requests())
		if v, _ := asstRun(t, "--text", "go", "--tool-choice", tc.force); v.State != "done" {
			t.Fatalf("%s/%s: %+v %q", tc.model, tc.force, v, v.err())
		}
		requests := api.Requests()[start:]
		if len(requests) != 2 {
			t.Fatalf("%s/%s: %d requests", tc.model, tc.force, len(requests))
		}
		got := ""
		if choice, ok := requests[0].JSON(t)["tool_choice"]; ok {
			raw, _ := json.Marshal(choice)
			got = string(raw)
		}
		if got != tc.want {
			t.Errorf("%s/%s: first tool_choice %s, want %q", tc.model, tc.force, got, tc.want)
		}
		if _, ok := requests[1].JSON(t)["tool_choice"]; ok {
			t.Errorf("%s/%s: the follow-up request kept tool_choice", tc.model, tc.force)
		}
	}
}

func asstIterations(t *testing.T, raw string) []inference.Iteration {
	t.Helper()
	var iters []inference.Iteration
	if err := json.Unmarshal([]byte(raw), &iters); err != nil {
		t.Fatalf("iterations %s: %v", raw, err)
	}
	return iters
}

func TestAsstUsageLedgerPricesFallbacksFlagsUnpricedModelsAndKeepsPartialRows(t *testing.T) {
	t.Parallel()
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	asstWarm(t)
	d := asstDB(t, dir)
	asstSet(t, "default_model", pricing.OpusID)

	api.Push(anthropictest.Reply("end_turn", anthropictest.Fallback(pricing.OpusID, pricing.HaikuID, "cyber"), anthropictest.Text("served by the fallback")).
		WithUsage(anthropictest.Usage{Input: 1000, Output: 100, Iterations: []anthropictest.Iteration{
			{Type: "message", Input: 600, Output: 10},
			{Type: "fallback_message", Model: pricing.HaikuID, Input: 400, Output: 90},
		}}))
	served, _ := asstRun(t, "--text", "go")
	if served.State != "done" {
		t.Fatalf("fallback run: %+v %q", served, served.err())
	}
	var model, raw string
	var input, output, unpriced int64
	var cost float64
	if err := d.Read.QueryRow("SELECT model, input_tokens, output_tokens, cost_usd, unpriced, iterations FROM usage WHERE run_id = ?", served.ID).
		Scan(&model, &input, &output, &cost, &unpriced, &raw); err != nil {
		t.Fatal(err)
	}
	iters := asstIterations(t, raw)
	if model != pricing.OpusID || input != 1000 || output != 100 || unpriced != 0 || len(iters) != 2 || iters[0].Model != pricing.OpusID || iters[1].Model != pricing.HaikuID {
		t.Fatalf("fallback row %s %d %d %d %+v", model, input, output, unpriced, iters)
	}
	wantOpus, err := pricing.CostUSD(pricing.OpusID, pricing.Tokens{Input: 600, Output: 10}, 0)
	if err != nil {
		t.Fatal(err)
	}
	wantHaiku, err := pricing.CostUSD(pricing.HaikuID, pricing.Tokens{Input: 400, Output: 90}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if iters[0].CostUSD != wantOpus || iters[1].CostUSD != wantHaiku || iters[1].Input != 400 || iters[1].Output != 90 {
		t.Errorf("iterations %+v, want costs %v and %v", iters, wantOpus, wantHaiku)
	}
	if math.Abs(cost-(wantOpus+wantHaiku)) > 1e-12 || math.Abs(served.Cost-(wantOpus+wantHaiku)) > 1e-12 {
		t.Errorf("row cost %v, run cost %v, want %v", cost, served.Cost, wantOpus+wantHaiku)
	}

	api.Push(anthropictest.Reply("end_turn", anthropictest.Fallback(pricing.OpusID, "claude-mystery-9", "cyber"), anthropictest.Text("served")).
		WithUsage(anthropictest.Usage{Input: 1000, Output: 100, Iterations: []anthropictest.Iteration{
			{Type: "message", Input: 600, Output: 10},
			{Type: "fallback_message", Model: "claude-mystery-9", Input: 400, Output: 90},
		}}))
	mystery, _ := asstRun(t, "--text", "go")
	if err := d.Read.QueryRow("SELECT iterations, unpriced, cost_usd FROM usage WHERE run_id = ?", mystery.ID).Scan(&raw, &unpriced, &cost); err != nil {
		t.Fatal(err)
	}
	if iters := asstIterations(t, raw); unpriced != 1 || len(iters) != 2 || !iters[1].Unpriced || iters[1].CostUSD <= 0 || cost <= 0 {
		t.Errorf("unknown serving model row: unpriced %d %+v cost %v", unpriced, iters, cost)
	}

	api.Push(anthropictest.StreamFailure("api_error", "connection dropped").WithUsage(anthropictest.Usage{Input: 500, Output: 20}))
	aborted, _ := asstRun(t, "--text", "go")
	if aborted.State != "failed" {
		t.Fatalf("aborted stream run: %+v", aborted)
	}
	var requestID string
	var partial int64
	if err := d.Read.QueryRow("SELECT request_id, input_tokens, output_tokens, partial, cost_usd FROM usage WHERE run_id = ?", aborted.ID).
		Scan(&requestID, &input, &output, &partial, &cost); err != nil {
		t.Fatal(err)
	}
	if requestID == "" || input != 500 || output != 20 || partial != 1 || cost <= 0 {
		t.Errorf("partial row %q %d %d %d %v", requestID, input, output, partial, cost)
	}

	base := startServeIn(t, dir)
	w := asstBrowse(t, base)
	_, page, _ := w.get(fmt.Sprintf("/chat/%d", served.ThreadID), false)
	for _, want := range []string{pricing.OpusID + " declined; " + pricing.HaikuID + " took over (refusal category: cyber).", "served by the fallback"} {
		if !strings.Contains(page, want) {
			t.Errorf("thread page lacks %q", want)
		}
	}
	_, page, _ = w.get("/settings/usage", false)
	for _, want := range []string{"1 unpriced", "1 partial"} {
		if !strings.Contains(page, want) {
			t.Errorf("usage page lacks %q", want)
		}
	}
}

var asstColdTokens = regexp.MustCompile(`about (\d+) tokens`)

func TestAsstRunCostCapAndSpendLimitStopRunsBeforeMoreIsSpent(t *testing.T) {
	t.Parallel()
	_, api, _ := moduleFixture(t, flagsExcept("", ""))
	asstWarm(t)
	asstSet(t, "default_model", pricing.OpusID)
	asstSet(t, "max_run_cost_usd_opus_5_5", "0.0001")

	sent := len(api.Requests())
	refused, _ := asstRun(t, "--text", "hi")
	if refused.State != "failed" || !strings.Contains(refused.err(), "run cost cap") || !strings.Contains(refused.err(), "max_run_cost_usd_opus_5_5") || len(api.Requests()) != sent {
		t.Fatalf("run over the cap: %+v %q, %d requests sent", refused, refused.err(), len(api.Requests())-sent)
	}
	found := asstColdTokens.FindStringSubmatch(refused.err())
	if found == nil {
		t.Fatalf("no token count in %q", refused.err())
	}
	tokens, err := strconv.ParseFloat(found[1], 64)
	if err != nil {
		t.Fatal(err)
	}
	fiveMinutes, oneHour := tokens*5/1e6, tokens*8/1e6
	asstSet(t, "max_run_cost_usd_opus_5_5", strconv.FormatFloat((fiveMinutes+oneHour)/2, 'f', -1, 64))

	asstSet(t, "cache_ttl_chat", "5m")
	api.Push(asstReply("sent"))
	if ran, _ := asstRun(t, "--text", "hi"); ran.State != "done" || len(api.Requests()) != sent+1 {
		t.Fatalf("5m run under the cap: %+v %q, %d requests", ran, ran.err(), len(api.Requests())-sent)
	}
	asstSet(t, "cache_ttl_chat", "1h")
	if held, _ := asstRun(t, "--text", "hi"); held.State != "failed" || !strings.Contains(held.err(), "run cost cap") || len(api.Requests()) != sent+1 {
		t.Fatalf("1h run: %+v %q, want the dearer 1h write to hit the cap before sending", held, held.err())
	}

	if ran := runApp(t, "settings", "unset", "max_run_cost_usd_opus_5_5"); ran.code != cli.ExitOK {
		t.Fatalf("settings unset: %d %s", ran.code, ran.stderr)
	}
	api.Push(anthropictest.Failure(http.StatusTooManyRequests, "rate_limit_error", "You have reached your specified API usage limits."))
	sent = len(api.Requests())
	if stopped, _ := asstRun(t, "--text", "hi"); stopped.State != "failed" || !strings.Contains(stopped.err(), "Anthropic spend limit reached") || len(api.Requests()) != sent+1 {
		t.Fatalf("spend-limit 429: %+v %q, %d requests, want one unretried request", stopped, stopped.err(), len(api.Requests())-sent)
	}
	limited := anthropictest.Failure(http.StatusTooManyRequests, "rate_limit_error", "slow down")
	limited.Header = http.Header{"Retry-After": {"30"}, "X-Should-Retry": {"false"}}
	api.Push(limited)
	if other, _ := asstRun(t, "--text", "hi"); other.State != "failed" || strings.Contains(other.err(), "spend limit") {
		t.Fatalf("ordinary 429: %+v %q, want a failure that is not a budget stop", other, other.err())
	}
}

func TestAsstWebFetchRefusesUntypedAndNonPublicURLs(t *testing.T) {
	t.Parallel()
	var hits atomic.Int32
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte("<html><body><p>private page</p></body></html>"))
	}))
	t.Cleanup(site.Close)
	_, api, d, w := asstServe(t)

	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("w1", "web_fetch", map[string]any{"url": site.URL + "/page"})), asstReply("blocked"))
	local := w.start(map[string]string{"text": "fetch " + site.URL + "/page"})
	asstSettleThread(t, d, local)
	if result := asstToolResult(t, d, local); !strings.Contains(result, "non-public address") || strings.Contains(result, "private page") {
		t.Errorf("loopback fetch result %s", result)
	}
	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("w2", "web_fetch", map[string]any{"url": site.URL + "/page"})), asstReply("blocked"))
	untyped := w.start(map[string]string{"text": "fetch the page I mentioned"})
	asstSettleThread(t, d, untyped)
	if result := asstToolResult(t, d, untyped); !strings.Contains(result, "url is not allowed") {
		t.Errorf("untyped fetch result %s", result)
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("the refused fetches reached the server %d times", n)
	}
}

func TestAsstSchedulerReportsToSystemdAndExitsZeroOnCancel(t *testing.T) {
	dir, _, f := moduleFixture(t, flagsExcept("", ""))
	notify := sdnotifytest.Listen(t, 2*time.Second)
	for name, value := range notify.Env() {
		setEnv(t, name, value)
	}
	asstWarm(t)
	d := asstDB(t, dir)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan appRun, 1)
	go func() { done <- runAppContext(t, f, ctx, "agent", "scheduler") }()
	for deadline := time.Now().Add(15 * time.Second); asstCount(t, d, "SELECT COUNT(*) FROM jobs WHERE kind = 'nightly'") != 1; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the first tick never seeded the nightly job")
		}
	}
	notify.Wait("READY=1")
	notify.Wait("STATUS=scheduling")
	notify.Wait("WATCHDOG=1")
	cancel()
	notify.Wait("STOPPING=1")
	select {
	case ran := <-done:
		if ran.code != cli.ExitOK {
			t.Fatalf("scheduler exit %d: %s", ran.code, ran.stderr)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the scheduler ignored cancellation")
	}
}

func TestAsstSchedulerResumesAnInterruptedRunAndTheOriginalLoopStandsDown(t *testing.T) {
	dir, api, f := moduleFixture(t, flagsExcept("", ""))
	asstWarm(t)
	d := asstDB(t, dir)
	seedCtx, seedCancel := context.WithCancel(context.Background())
	seeded := make(chan appRun, 1)
	go func() { seeded <- runAppContext(t, f, seedCtx, "agent", "scheduler") }()
	for deadline := time.Now().Add(15 * time.Second); asstCount(t, d, "SELECT COUNT(*) FROM jobs WHERE kind = 'nightly'") != 1; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			seedCancel()
			t.Fatal("the scheduler never seeded its jobs")
		}
	}
	seedCancel()
	<-seeded
	asstExec(t, d, "UPDATE jobs SET next_due = NULL")

	api.Push(
		anthropictest.Reply("tool_use", anthropictest.ToolUse("call-1", "task_add", map[string]any{"title": "Renew the passport"})),
		asstReply("done"),
		anthropictest.Reply("tool_use", anthropictest.ToolUse("call-1", "task_add", map[string]any{"title": "Renew the passport"})),
	)
	arrived := make(chan struct{}, 3)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseOriginal := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseOriginal)
	var first atomic.Bool
	logRequest := api.OnRequest
	api.OnRequest = func(r anthropictest.Request) {
		logRequest(r)
		hold := first.CompareAndSwap(false, true)
		select {
		case arrived <- struct{}{}:
		default:
		}
		if hold {
			<-release
		}
	}

	runDone := make(chan appRun, 1)
	go func() {
		runDone <- asstRunApp(asstApp(f), context.Background(), "agent", "run", "--text", "renew it", "--kind", "cron")
	}()
	<-arrived
	var run int64
	if err := d.Read.QueryRow("SELECT MAX(id) FROM runs").Scan(&run); err != nil {
		t.Fatal(err)
	}
	asstExec(t, d, "UPDATE runs SET lease_until = 0 WHERE id = ?", run)

	ctx, cancel := context.WithCancel(context.Background())
	schedDone := make(chan appRun, 1)
	go func() { schedDone <- runAppContext(t, f, ctx, "agent", "scheduler") }()
	for range 2 {
		select {
		case <-arrived:
		case <-time.After(20 * time.Second):
			cancel()
			t.Fatal("the scheduler never resumed the interrupted run")
		}
	}
	for deadline := time.Now().Add(20 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		var state string
		if err := d.Read.QueryRow("SELECT state FROM runs WHERE id = ?", run).Scan(&state); err == nil && state == "done" {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("the resumed run never finished")
		}
	}
	releaseOriginal()
	select {
	case ran := <-runDone:
		if ran.code != cli.ExitOK {
			t.Fatalf("original agent run exit %d: %s", ran.code, ran.stderr)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the original loop never stopped")
	}
	cancel()
	<-schedDone
	if n := asstCount(t, d, "SELECT COUNT(*) FROM tasks WHERE title = 'Renew the passport'"); n != 1 {
		t.Errorf("the task was added %d times, want once", n)
	}
	if n := asstCount(t, d, "SELECT COUNT(*) FROM thread_messages m JOIN runs r ON r.thread_id = m.thread_id WHERE r.id = ? AND m.role = 'assistant'", run); n != 2 {
		t.Errorf("%d assistant messages, want the resumed tool call and the final answer only", n)
	}
	if n := asstCount(t, d, "SELECT COUNT(*) FROM usage WHERE run_id = ?", run); n != 3 {
		t.Errorf("%d usage rows, want every model call accounted", n)
	}
}

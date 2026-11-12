package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/assistant/pricing"
	"github.com/jeiang/atrium/internal/attention"
	"github.com/jeiang/atrium/internal/attention/sources"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/inference"
	"github.com/jeiang/atrium/internal/serverops"
	"github.com/jeiang/atrium/internal/settings"
)

func opsClassifierFixture(t *testing.T, kv map[string]string) (string, *fakeOpsGrafana, *modelLog, *fixture, *db.DB) {
	t.Helper()
	fake := &fakeOpsGrafana{up: 1}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	dir, api, f := moduleFixture(t, flagsExcept("", ""))
	setEnv(t, "ATRIUM_HOST_CONFIG", grafanaHost(t, dir, srv.URL))
	setEnv(t, settingEnv(serverops.KeyHosts), "node-a")
	if len(kv) > 0 {
		seedSettings(t, kv)
	}
	tickOpsSampler(t, f, nil)
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
	t.Cleanup(func() { _ = d.Close() })
	var samples int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM ops_samples").Scan(&samples); err != nil || samples < 6 {
		t.Fatalf("baseline samples = %d, %v", samples, err)
	}
	return dir, fake, api, f, d
}

func tickOpsSampler(t *testing.T, f *fixture, d *db.DB) {
	t.Helper()
	if d == nil {
		if ran := runAppContext(t, f, context.Background(), "agent", "tick"); ran.code != cli.ExitOK {
			t.Fatalf("first agent tick: %d %s", ran.code, ran.stderr)
		}
		path, _ := f.lookup("ATRIUM_DB")
		d = mustConnect(t, path, keyFor(t))
		defer func() { _ = d.Close() }()
	}
	var before int64
	if err := d.Read.QueryRow("SELECT COALESCE(MAX(id), 0) FROM job_runs").Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.Exec("UPDATE jobs SET state = 'active', next_due = ?, lease_owner = NULL, lease_until = NULL WHERE name = ?",
		float64(time.Now().Unix())-1, serveropsJobName); err != nil {
		t.Fatal(err)
	}
	if ran := runAppContext(t, f, context.Background(), "agent", "tick"); ran.code != cli.ExitOK {
		t.Fatalf("agent tick: %d %s", ran.code, ran.stderr)
	}
	var runs int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM job_runs WHERE id > ? AND ended_at IS NOT NULL AND job_id = (SELECT id FROM jobs WHERE name = ?)",
		before, serveropsJobName).Scan(&runs); err != nil || runs != 1 {
		t.Fatalf("ops-sampler runs in the tick = %d, %v, want one", runs, err)
	}
}

func lastSamplerStatus(t *testing.T, d *db.DB) string {
	t.Helper()
	var status string
	if err := d.Read.QueryRow("SELECT status FROM job_runs WHERE job_id = (SELECT id FROM jobs WHERE name = ?) ORDER BY id DESC LIMIT 1",
		serveropsJobName).Scan(&status); err != nil {
		t.Fatal(err)
	}
	return status
}

func classificationUsage(t *testing.T, d *db.DB) (count int, settled int, accounted int64) {
	t.Helper()
	if err := d.Read.QueryRow("SELECT COUNT(*), COALESCE(SUM(state = 'settled'), 0), COALESCE(SUM(accounted_nusd), 0) FROM usage WHERE source = ?",
		"classifier:ops_condenser").Scan(&count, &settled, &accounted); err != nil {
		t.Fatal(err)
	}
	return count, settled, accounted
}

func classificationItems(t *testing.T, f *fixture, d *db.DB) []attention.Item {
	t.Helper()
	ctx := config.With(settings.With(context.Background(), f.settings), f.state)
	result, err := attention.Query(ctx, d.Read, float64(time.Now().Unix()), sources.ClassificationBudget())
	if err != nil {
		t.Fatal(err)
	}
	return result.Items
}

func deferralCount(t *testing.T, d *db.DB) int {
	t.Helper()
	var n int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM inference_classification_deferrals").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func hostDownFinding() anthropictest.Response {
	return anthropictest.JSON(map[string]any{"findings": []map[string]any{
		{"fact": "node-a:up", "severity": "warning", "title": "Host node-a is down", "body": "up changed from up to down."},
		{"fact": "node-a:cpu_pct", "severity": "warning", "title": "CPU high", "body": "cpu_pct is not in the change set."},
	}})
}

func TestOpsSamplerClassifiesAChangeInOneFixedHaikuRequest(t *testing.T) {
	dir, fake, api, f, d := opsClassifierFixture(t, map[string]string{inference.ThreadCapKey: "0.000001"})
	before := len(api.Requests())
	fake.setUp(0)
	fake.setAlerts([]map[string]any{{"state": "firing", "labels": map[string]any{"alertname": "HighCPU", "instance": "node-a:9100"}}})
	api.PushJSON(hostDownFinding())
	tickOpsSampler(t, f, d)
	requests := api.Requests()
	if len(requests) != before+1 {
		t.Fatalf("model requests for one change = %d, want one", len(requests)-before)
	}
	body := requests[before].JSON(t)
	if body["model"] != pricing.HaikuID {
		t.Errorf("model = %v, want %s", body["model"], pricing.HaikuID)
	}
	for _, key := range []string{"tools", "tool_choice", "thinking", "context_management"} {
		if _, ok := body[key]; ok {
			t.Errorf("classification request carries %s", key)
		}
	}
	if max, _ := body["max_tokens"].(float64); max != 2000 {
		t.Errorf("max_tokens = %v, want 2000", body["max_tokens"])
	}
	if system, _ := body["system"].(string); system != serverops.CondenserSystem {
		t.Errorf("system = %v", body["system"])
	}
	output, _ := body["output_config"].(map[string]any)
	format, _ := output["format"].(map[string]any)
	if format["type"] != "json_schema" {
		t.Fatalf("output_config = %v, want a json_schema format", body["output_config"])
	}
	schema, err := json.Marshal(format["schema"])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(schema), `"findings"`) || strings.Contains(string(schema), "maxItems") || strings.Contains(string(schema), "maxLength") ||
		!strings.Contains(string(schema), "At most 10 items.") || !strings.Contains(string(schema), "At most 120 characters.") {
		t.Errorf("wire schema = %s", schema)
	}
	messages, _ := body["messages"].([]any)
	if len(messages) != 1 {
		t.Fatalf("messages = %d, want one", len(messages))
	}
	user, _ := messages[0].(map[string]any)["content"].(string)
	if user == "" {
		raw, _ := json.Marshal(messages[0])
		user = string(raw)
	}
	if !strings.Contains(user, "OPS_DELTA ") || !strings.Contains(user, "node-a:up") || !strings.Contains(user, "HighCPU") {
		t.Errorf("classification input = %q", user)
	}
	if count, settled, accounted := classificationUsage(t, d); count != 1 || settled != 1 || accounted <= 0 {
		t.Errorf("classification usage = %d rows, %d settled, %d nusd; want one settled row with a cost", count, settled, accounted)
	}
	var runState, threadKind string
	if err := d.Read.QueryRow("SELECT r.state, th.kind FROM runs r JOIN threads th ON th.id = r.thread_id WHERE r.kind = 'classification'").
		Scan(&runState, &threadKind); err != nil || runState != "done" || threadKind != "classification" {
		t.Errorf("classification run = %q on a %q thread, %v", runState, threadKind, err)
	}
	var severity, title string
	var marks int64
	if err := d.Read.QueryRow("SELECT severity, title, marks FROM ops_findings WHERE fact = 'node-a:up'").Scan(&severity, &title, &marks); err != nil ||
		severity != "warning" || title != "Host node-a is down" || marks != 1 {
		t.Errorf("finding = %q %q %d, %v", severity, title, marks, err)
	}
	if _, _, found := workerFinding(t, d, "node-a:cpu_pct"); found {
		t.Errorf("a finding outside the change set was written")
	}
	opsItems, err := attention.Query(config.With(settings.With(context.Background(), f.settings), f.state), d.Read,
		float64(time.Now().Unix()), serverops.AttentionSource(fixtureEnv(f)))
	if err != nil {
		t.Fatal(err)
	}
	if len(opsItems.Items) != 1 || opsItems.Items[0].Kind != "ops_finding" || !strings.Contains(opsItems.Items[0].Title, "node-a") {
		t.Errorf("ops attention = %+v, want the node-a finding", opsItems.Items)
	}
	var workerRuns int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM runs WHERE kind = 'worker'").Scan(&workerRuns); err != nil || workerRuns != 0 {
		t.Errorf("worker runs = %d, %v, want none", workerRuns, err)
	}
	if status := lastSamplerStatus(t, d); status != "ok" {
		t.Errorf("sampler status = %q, want ok", status)
	}
	quiet := len(api.Requests())
	tickOpsSampler(t, f, d)
	if len(api.Requests()) != quiet {
		t.Errorf("a pass with no new change made %d model calls", len(api.Requests())-quiet)
	}
	fake.setUp(1)
	fake.setAlerts(nil)
	api.PushJSON(anthropictest.JSON(map[string]any{"findings": []any{}}))
	tickOpsSampler(t, f, d)
	if _, resolved, found := workerFinding(t, d, "node-a:up"); !found || !resolved {
		t.Errorf("finding after recovery: found %v resolved %v", found, resolved)
	}
	if left := api.JSONRemaining(); left != 0 {
		t.Errorf("unused classification replies = %d", left)
	}

	base := startServeIn(t, dir)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar, Timeout: time.Minute}
	if status, page, _ := fetch(t, client, http.MethodGet, base+"/chat", nil); status != http.StatusOK || strings.Contains(page, "Classification") {
		t.Errorf("GET /chat = %d, classification thread listed: %v", status, strings.Contains(page, "Classification"))
	}
	if ran := runSettings(t, "set", inference.ThreadCapKey, "10"); ran.code != cli.ExitOK {
		t.Fatalf("raise the thread cap: %d %s", ran.code, ran.stderr)
	}
	warm(t, api)
	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("s1", "search_threads", map[string]any{"query": "ops_condenser"})))
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("searched")))
	searchFrom := len(api.Requests())
	if ran := runApp(t, "agent", "run", "--text", "search memory"); ran.code != cli.ExitOK {
		t.Fatalf("memory search run: %d %s", ran.code, ran.stderr)
	}
	result := toolResultFor(t, api.Requests()[searchFrom+1], "s1")
	if raw, _ := json.Marshal(result); strings.Contains(string(raw), "Classification") || strings.Contains(string(raw), "classification") {
		t.Errorf("memory search returned a classification thread: %s", raw)
	}
}

func TestOpsInvalidReplyIsSettledAndRetriedOnce(t *testing.T) {
	_, fake, api, f, d := opsClassifierFixture(t, nil)
	before := len(api.Requests())
	fake.setUp(0)
	api.PushJSON(anthropictest.Reply("end_turn", anthropictest.Text(
		`{"findings":[{"fact":"node-a:up","severity":"urgent","title":"Host node-a is down","body":"down"}]}`)).
		WithUsage(anthropictest.Usage{Input: 900, Output: 40}))
	tickOpsSampler(t, f, d)
	if got := len(api.Requests()) - before; got != 1 {
		t.Fatalf("requests = %d, want one", got)
	}
	if count, settled, accounted := classificationUsage(t, d); count != 1 || settled != 1 || accounted <= 0 {
		t.Errorf("classification usage after a schema violation = %d rows, %d settled, %d nusd", count, settled, accounted)
	}
	var runState, runErr string
	if err := d.Read.QueryRow("SELECT state, COALESCE(error, '') FROM runs WHERE kind = 'classification'").Scan(&runState, &runErr); err != nil ||
		runState != "failed" || runErr != "malformed_output" {
		t.Errorf("classification run = %q %q, %v", runState, runErr, err)
	}
	if _, _, found := workerFinding(t, d, "node-a:up"); found {
		t.Errorf("a schema violation filed a finding")
	}
	if status := lastSamplerStatus(t, d); status != "error" {
		t.Errorf("sampler status after a schema violation = %q, want error", status)
	}
	api.PushJSON(hostDownFinding())
	tickOpsSampler(t, f, d)
	if got := len(api.Requests()) - before; got != 2 {
		t.Fatalf("requests after the retry = %d, want two", got)
	}
	if _, _, found := workerFinding(t, d, "node-a:up"); !found {
		t.Errorf("the retry filed no finding")
	}
	quiet := len(api.Requests())
	tickOpsSampler(t, f, d)
	if len(api.Requests()) != quiet {
		t.Errorf("a settled retry was retried again: %d calls", len(api.Requests())-quiet)
	}
}

func TestOpsMonthlyBudgetRefusalDefersAndRaisesAttention(t *testing.T) {
	dir, fake, api, f, d := opsClassifierFixture(t, map[string]string{inference.BudgetKey: "0.00102"})
	before := len(api.Requests())
	fake.setUp(0)
	tickOpsSampler(t, f, d)
	if got := len(api.Requests()) - before; got != 0 {
		t.Fatalf("a refused classification reached the provider %d times", got)
	}
	if count, _, _ := classificationUsage(t, d); count != 0 {
		t.Errorf("a refused classification left %d usage rows", count)
	}
	if n := deferralCount(t, d); n != 1 {
		t.Errorf("deferrals = %d, want one", n)
	}
	if status := lastSamplerStatus(t, d); status != "skipped_budget" {
		t.Errorf("sampler status = %q, want skipped_budget", status)
	}
	items := classificationItems(t, f, d)
	if len(items) != 1 || items[0].Kind != "classification_budget" || !strings.Contains(items[0].Detail, "/settings/usage") ||
		!strings.Contains(items[0].Detail, "ops_condenser") {
		t.Fatalf("attention = %+v, want one classification_budget item linking /settings/usage", items)
	}
	tickOpsSampler(t, f, d)
	if got := len(api.Requests()) - before; got != 0 {
		t.Errorf("the pending retry reached the provider %d times while refused", got)
	}
	base := startServeIn(t, dir)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar, Timeout: time.Minute}
	if status, page, _ := fetch(t, client, http.MethodGet, base+"/settings/usage", nil); status != http.StatusOK || !strings.Contains(page, "Monthly budget") {
		t.Errorf("GET /settings/usage = %d", status)
	}
	if ran := runSettings(t, "set", inference.BudgetKey, "150"); ran.code != cli.ExitOK {
		t.Fatalf("raise the budget: %d %s", ran.code, ran.stderr)
	}
	if items := classificationItems(t, f, d); len(items) != 0 {
		t.Errorf("attention after raising the budget = %+v, want none", items)
	}
	api.PushJSON(hostDownFinding())
	tickOpsSampler(t, f, d)
	if got := len(api.Requests()) - before; got != 1 {
		t.Fatalf("requests after the budget has room = %d, want one", got)
	}
	if _, _, found := workerFinding(t, d, "node-a:up"); !found {
		t.Errorf("the change was not classified after the budget had room")
	}
	if n := deferralCount(t, d); n != 1 {
		t.Errorf("deferral history = %d, want the one row kept", n)
	}
}

func TestOpsSourceCapRefusesWithoutADeferral(t *testing.T) {
	_, fake, api, f, d := opsClassifierFixture(t, map[string]string{serverops.KeyWorkerMonthlyUSD: "0.0001"})
	before := len(api.Requests())
	fake.setUp(0)
	tickOpsSampler(t, f, d)
	if got := len(api.Requests()) - before; got != 0 {
		t.Fatalf("a source-capped classification reached the provider %d times", got)
	}
	if n := deferralCount(t, d); n != 0 {
		t.Errorf("deferrals after a source cap refusal = %d, want none", n)
	}
	if items := classificationItems(t, f, d); len(items) != 0 {
		t.Errorf("attention after a source cap refusal = %+v, want none", items)
	}
	if status := lastSamplerStatus(t, d); status != "skipped_budget" {
		t.Errorf("sampler status = %q, want skipped_budget", status)
	}
	var runErr string
	if err := d.Read.QueryRow("SELECT COALESCE(error, '') FROM runs WHERE kind = 'classification'").Scan(&runErr); err != nil || runErr != "source_cap" {
		t.Errorf("classification run error = %q, %v", runErr, err)
	}
}

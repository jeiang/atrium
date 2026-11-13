package main

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/serverops"
)

func classificationRequests(t *testing.T, api *modelLog) []anthropictest.Request {
	t.Helper()
	var out []anthropictest.Request
	for _, r := range api.Requests() {
		if r.Path != "/v1/messages" {
			continue
		}
		if _, ok := r.JSON(t)["output_config"]; ok {
			out = append(out, r)
		}
	}
	return out
}

func samplerRuns(t *testing.T, d *db.DB) int {
	t.Helper()
	var n int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM job_runs WHERE ended_at IS NOT NULL AND job_id = (SELECT id FROM jobs WHERE name = ?)",
		serveropsJobName).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func dueSampler(t *testing.T, d *db.DB) {
	t.Helper()
	if _, err := d.Write.Exec("UPDATE jobs SET next_due = NULL WHERE name <> ?", serveropsJobName); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.Exec("UPDATE jobs SET state = 'active', next_due = ?, lease_owner = NULL, lease_until = NULL WHERE name = ?",
		float64(time.Now().Unix())-1, serveropsJobName); err != nil {
		t.Fatal(err)
	}
}

func awaitSamplerRun(t *testing.T, d *db.DB, after int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		if samplerRuns(t, d) > after {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("the scheduler did not run the ops sampler")
}

func TestOpsClassifierUsesTheCurrentAnthropicKeyInALongLivedScheduler(t *testing.T) {
	fake := &fakeOpsGrafana{up: 1}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	dir, api, f := moduleFixture(t, flagsExcept("", ""))
	host := grafanaHost(t, dir, srv.URL)
	raw, err := os.ReadFile(host)
	if err != nil {
		t.Fatal(err)
	}
	shared := strconv.Quote(filepath.Join(testRoot, "host", "anthropic.key"))
	if !strings.Contains(string(raw), shared) {
		t.Fatalf("the host config does not name the shared Anthropic key file")
	}
	keyPath := filepath.Join(dir, "anthropic-ops.key")
	if err := os.WriteFile(keyPath, []byte("key-one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(host, []byte(strings.Replace(string(raw), shared, strconv.Quote(keyPath), 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	setEnv(t, "ATRIUM_HOST_CONFIG", host)
	setEnv(t, settingEnv(serverops.KeyHosts), "node-a")
	tickOpsSampler(t, f, nil)
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
	t.Cleanup(func() { _ = d.Close() })

	fake.setUp(0)
	api.PushJSON(hostDownFinding())
	dueSampler(t, d)
	runs := samplerRuns(t, d)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan appRun, 1)
	go func() { done <- runAppContext(t, f, ctx, "agent", "scheduler") }()
	defer func() {
		cancel()
		<-done
	}()
	awaitSamplerRun(t, d, runs)
	sent := classificationRequests(t, api)
	if len(sent) != 1 || sent[0].Header.Get("X-Api-Key") != "key-one" {
		t.Fatalf("first classification: %d requests, key matches key-one: %v", len(sent), len(sent) == 1 && sent[0].Header.Get("X-Api-Key") == "key-one")
	}

	if err := os.WriteFile(keyPath, []byte("key-two\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fake.setUp(1)
	api.PushJSON(anthropictest.JSON(map[string]any{"findings": []map[string]any{}}))
	runs = samplerRuns(t, d)
	dueSampler(t, d)
	awaitSamplerRun(t, d, runs)
	sent = classificationRequests(t, api)
	if len(sent) != 2 || sent[1].Header.Get("X-Api-Key") != "key-two" {
		t.Fatalf("after rotation: %d requests, the last uses the rotated key: %v", len(sent), len(sent) == 2 && sent[1].Header.Get("X-Api-Key") == "key-two")
	}

	if err := os.Remove(keyPath); err != nil {
		t.Fatal(err)
	}
	fake.setUp(0)
	api.PushJSON(hostDownFinding())
	runs = samplerRuns(t, d)
	dueSampler(t, d)
	awaitSamplerRun(t, d, runs)
	if sent = classificationRequests(t, api); len(sent) != 2 {
		t.Fatalf("a removed key still made %d paid classification requests", len(sent)-2)
	}
	if status := lastSamplerStatus(t, d); status != "skipped_unconfigured" {
		t.Errorf("sampler status with the key removed = %q, want skipped_unconfigured", status)
	}
}

func TestDreamingLeavesARefusedClassificationOutOfItsSignals(t *testing.T) {
	_, fake, api, f, d := opsClassifierFixture(t, map[string]string{serverops.KeyWorkerMonthlyUSD: "0.0001"})
	fake.setUp(0)
	tickOpsSampler(t, f, d)
	var refused int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM runs WHERE kind = 'classification' AND state = 'failed' AND error = 'source_cap'").Scan(&refused); err != nil || refused != 1 {
		t.Fatalf("refused classification runs = %d, %v, want one", refused, err)
	}
	if ran := runApp(t, "agent", "run", "--text", "I prefer green tea"); ran.code != cli.ExitOK {
		t.Fatalf("chat run: %d %s", ran.code, ran.stderr)
	}
	source := chatThread(t, d)
	now := float64(time.Now().Unix())
	if _, err := d.Write.Exec("INSERT INTO runs (thread_id, kind, model, effort, state, error, started_at, ended_at) VALUES (?, 'chat', 'claude-haiku-5-5', 'low', 'failed', 'chat failure 5521', ?, ?)",
		source, now-60, now-30); err != nil {
		t.Fatal(err)
	}
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("nothing to write")))
	requests := len(api.Requests())
	dreamingRun(t, f, d)
	if len(api.Requests()) <= requests {
		t.Fatalf("the nightly run made no model request")
	}
	text := requestText(t, api.Requests()[requests])
	if !strings.Contains(text, "chat failure 5521") {
		t.Fatalf("the dreaming prompt lacks the failed chat run signal: %q", text)
	}
	if strings.Contains(text, "source_cap") || strings.Contains(text, "failed classification run") {
		t.Errorf("the dreaming prompt carries the refused classification: %q", text)
	}
}

func TestRecoveryClosesAStaleClassificationRun(t *testing.T) {
	dir, api, f := moduleFixture(t, flagsExcept("", ""))
	warm(t, api)
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
	defer func() { _ = d.Close() }()
	now := float64(time.Now().Unix())
	res, err := d.Write.Exec("INSERT INTO threads (kind, title, effort, created_at, updated_at, role, model, thinking, web_search) VALUES ('classification', 'Classification: ops_condenser', '', ?, ?, 'classification:ops_condenser', 'claude-haiku-5-5', 'disabled', 0)", now, now)
	if err != nil {
		t.Fatal(err)
	}
	thread, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	insert := func(started float64) int64 {
		t.Helper()
		res, err := d.Write.Exec("INSERT INTO runs (thread_id, kind, model, effort, state, started_at, limits_json) VALUES (?, 'classification', 'claude-haiku-5-5', '', 'accounting', ?, '{\"item_key\":\"ops:stale\"}')", thread, started)
		if err != nil {
			t.Fatal(err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	stale := insert(now - 7200)
	fresh := insert(now - 5)
	if ran := runAppContext(t, f, context.Background(), "agent", "tick"); ran.code != cli.ExitOK {
		t.Fatalf("agent tick: %d %s", ran.code, ran.stderr)
	}
	state := func(id int64) (string, string, bool) {
		t.Helper()
		var s, e string
		var ended *float64
		if err := d.Read.QueryRow("SELECT state, COALESCE(error, ''), ended_at FROM runs WHERE id = ?", id).Scan(&s, &e, &ended); err != nil {
			t.Fatal(err)
		}
		return s, e, ended != nil
	}
	if s, e, ended := state(stale); s != "failed" || e != "crashed" || !ended {
		t.Errorf("stale accounting run = %s %q ended %v, want failed crashed", s, e, ended)
	}
	if s, _, ended := state(fresh); s != "accounting" || ended {
		t.Errorf("fresh accounting run = %s ended %v, want accounting", s, ended)
	}
}

func TestWorkerStatusReportsTheFixedSchemaMode(t *testing.T) {
	_, api, _ := moduleFixture(t, flagsExcept("", ""))
	warm(t, api)
	before := len(api.Requests())
	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("s1", "worker_status", map[string]any{})))
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("status")))
	if ran := runApp(t, "agent", "run", "--text", "ops status"); ran.code != cli.ExitOK {
		t.Fatalf("worker_status chat: %d %s", ran.code, ran.stderr)
	}
	if len(api.Requests()) < before+2 {
		t.Fatalf("worker_status chat made %d requests", len(api.Requests())-before)
	}
	if result := fmt.Sprint(toolResultFor(t, api.Requests()[before+1], "s1")); !strings.Contains(result, "fixed-schema-haiku") {
		t.Errorf("worker_status result lacks the classifier mode: %s", result)
	}
}

func TestWorkerFindingRefusesAnUnchangedFact(t *testing.T) {
	_, fake, api, f, d := opsClassifierFixture(t, nil)
	warm(t, api)
	fake.setUp(0)
	api.PushJSON(anthropictest.JSON(map[string]any{"findings": []map[string]any{}}))
	tickOpsSampler(t, f, d)
	thread, err := serverops.WorkerThreadID(context.Background(), d.Read)
	if err != nil || thread == 0 {
		t.Fatalf("ops worker thread = %d, %v", thread, err)
	}
	if _, err := d.Write.Exec("INSERT INTO runs (thread_id, kind, model, effort, state, started_at) VALUES (?, 'worker', 'claude-haiku-5-5', 'low', 'interrupted', ?)",
		thread, float64(time.Now().Unix())); err != nil {
		t.Fatal(err)
	}
	before := len(api.Requests())
	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("f1", "ops_finding", map[string]any{
		"fact": "node-a:cpu_pct", "severity": "warning", "title": "CPU high", "body": "cpu_pct changed.",
	})))
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("done")))
	if ran := runAppContext(t, f, context.Background(), "agent", "tick"); ran.code != cli.ExitOK {
		t.Fatalf("agent tick: %d %s", ran.code, ran.stderr)
	}
	deadline := time.Now().Add(time.Minute)
	for len(api.Requests()) < before+2 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if len(api.Requests()) < before+2 {
		t.Fatalf("the resumed worker run made %d requests, want two", len(api.Requests())-before)
	}
	if result := toolResultFor(t, api.Requests()[before+1], "f1"); result["is_error"] != true {
		t.Errorf("ops_finding on an unchanged fact = %v, want an error", result)
	}
	if _, _, found := workerFinding(t, d, "node-a:cpu_pct"); found {
		t.Errorf("ops_finding wrote a finding for an unchanged fact")
	}
}

func TestOpsSamplerNamesWhyTheAnthropicTransportCannotBeBuilt(t *testing.T) {
	dir, fake, api, f, d := opsClassifierFixture(t, nil)
	hostPath, _ := f.lookup("ATRIUM_HOST_CONFIG")
	raw, err := os.ReadFile(hostPath)
	if err != nil {
		t.Fatal(err)
	}
	key, err := os.ReadFile(filepath.Join(testRoot, "host", "anthropic.key"))
	if err != nil || strings.TrimSpace(string(key)) == "" || !strings.Contains(string(raw), "anthropic.key") {
		t.Fatalf("the host config does not name a readable shared Anthropic key: %v", err)
	}
	ca := filepath.Join(dir, "missing-ca.pem")
	setEnv(t, "ATRIUM_HOST_CONFIG", hostConfigFrom(t, hostPath, dir, "host-broken-ca.toml", map[string]string{
		`kind = "service/anthropic"`: `kind = "service/anthropic"` + "\ntransport = { ca_file = " + strconv.Quote(ca) + " }",
	}))
	before := len(classificationRequests(t, api))
	fake.setUp(0)
	tickOpsSampler(t, f, d)
	if sent := classificationRequests(t, api); len(sent) != before {
		t.Fatalf("an unbuildable transport sent %d paid classification requests", len(sent)-before)
	}
	var status, text string
	if err := d.Read.QueryRow("SELECT status, COALESCE(error, '') FROM job_runs WHERE job_id = (SELECT id FROM jobs WHERE name = ?) ORDER BY id DESC LIMIT 1",
		serveropsJobName).Scan(&status, &text); err != nil {
		t.Fatal(err)
	}
	if status != "skipped_unconfigured" {
		t.Errorf("sampler status with an unbuildable transport = %q, want skipped_unconfigured", status)
	}
	want := "the Anthropic connection transport cannot be built: CA bundle " + ca + " cannot be read"
	if !strings.Contains(text, want) || strings.Contains(text, "is not configured") {
		t.Errorf("sampler result text = %q, want the transport cause %q", text, want)
	}
	if strings.Contains(text, strings.TrimSpace(string(key))) {
		t.Errorf("sampler result text carries the API key")
	}
}

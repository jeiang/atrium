package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/assistant/prompts"
	grafanatools "github.com/jeiang/atrium/internal/assistant/tools/grafana"
	opstools "github.com/jeiang/atrium/internal/assistant/tools/ops"
	"github.com/jeiang/atrium/internal/attention"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/serverops"
	"github.com/jeiang/atrium/internal/settings"
	"github.com/jeiang/atrium/internal/tool"
)

func fixtureEnv(f *fixture) config.Env {
	return func(name string) (string, bool) {
		value, ok := f.env[name]
		return value, ok
	}
}

func samplerFinished(t *testing.T, d *db.DB) func() bool {
	t.Helper()
	var before int64
	if err := d.Read.QueryRow("SELECT COALESCE(MAX(id), 0) FROM job_runs").Scan(&before); err != nil {
		t.Fatal(err)
	}
	return func() bool {
		var count int
		if err := d.Read.QueryRow("SELECT COUNT(*) FROM job_runs WHERE id > ? AND job_id = (SELECT id FROM jobs WHERE name = 'ops-sampler') AND ended_at IS NOT NULL", before).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count > 0
	}
}

func samplesAtLeast(t *testing.T, d *db.DB, n int) func() bool {
	t.Helper()
	return func() bool {
		var count int
		if err := d.Read.QueryRow("SELECT COUNT(*) FROM ops_samples").Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count >= n
	}
}

func waitForOps(t *testing.T, d *db.DB, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(180 * time.Second); !ok(); {
		if time.Now().After(deadline) {
			var state, runErr string
			_ = d.Read.QueryRow("SELECT r.state, COALESCE(r.error, '') FROM runs r JOIN threads th ON th.id = r.thread_id WHERE th.kind = 'worker' ORDER BY r.id DESC LIMIT 1").Scan(&state, &runErr)
			var buf [1 << 20]byte
			n := runtime.Stack(buf[:], true)
			t.Fatalf("timed out waiting for %s (worker run: %s %s)\n%s", what, state, runErr, buf[:n])
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func workerFinding(t *testing.T, d *db.DB, fact string) (id int64, resolved bool, found bool) {
	t.Helper()
	var resolvedAt *float64
	err := d.Read.QueryRow("SELECT id, resolved_at FROM ops_findings WHERE fact = ? ORDER BY id DESC LIMIT 1", fact).Scan(&id, &resolvedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, false
	}
	if err != nil {
		t.Fatal(err)
	}
	return id, resolvedAt != nil, true
}

func workerToolNames() []string {
	var names []string
	for _, set := range [][]tool.Tool{opstools.Tools(), grafanatools.Tools()} {
		for _, def := range set {
			if def.Contexts.Has(tool.Worker) {
				names = append(names, def.Name)
			}
		}
	}
	sort.Strings(names)
	return names
}

func TestQuietPassMakesNoModelCall(t *testing.T) {
	fake := &fakeOpsGrafana{up: 1}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	dir, api, f := moduleFixture(t, flagsExcept("", ""))
	setEnv(t, settingEnv(grafanatools.KeyURL), srv.URL)
	setEnv(t, hostEnv("grafana", grafanatools.KeyMetricsUID), "metrics")
	setEnv(t, settingEnv(serverops.KeyHosts), "node-a")
	warm(t, api)
	requests := len(api.Requests())
	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
	defer func() { _ = d.Close() }()
	runOpsSampler(t, f, d, samplesAtLeast(t, d, 6))
	runOpsSampler(t, f, d, samplesAtLeast(t, d, 12))
	if len(api.Requests()) != requests {
		t.Fatalf("a quiet pass made %d model calls", len(api.Requests())-requests)
	}
}

func TestHostDownStartsOneWorkerRunAndFilesAFinding(t *testing.T) {
	fake := &fakeOpsGrafana{up: 1}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	dir, api, f := moduleFixture(t, flagsExcept("", ""))
	setEnv(t, settingEnv(grafanatools.KeyURL), srv.URL)
	setEnv(t, hostEnv("grafana", grafanatools.KeyMetricsUID), "metrics")
	setEnv(t, settingEnv(serverops.KeyHosts), "node-a")
	warm(t, api)
	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
	defer func() { _ = d.Close() }()
	runOpsSampler(t, f, d, samplesAtLeast(t, d, 6))
	before := len(api.Requests())
	fake.setUp(0)
	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("s1", "ops_samples", map[string]any{})))
	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("f1", "ops_finding", map[string]any{
		"fact": "node-a:up", "severity": "warning", "title": "Host node-a is down", "body": "up changed from up to down.",
	})))
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("filed")))
	finished := samplerFinished(t, d)
	runOpsSampler(t, f, d, func() bool {
		_, _, found := workerFinding(t, d, "node-a:up")
		return found && finished()
	})
	requests := api.Requests()
	if len(requests) < before+1 {
		t.Fatalf("worker requests = %d, want at least 1", len(requests)-before)
	}
	body := requests[before].JSON(t)
	var names []string
	for _, raw := range body["tools"].([]any) {
		names = append(names, fmt.Sprint(raw.(map[string]any)["name"]))
	}
	sort.Strings(names)
	if want := workerToolNames(); !slices.Equal(names, want) {
		t.Errorf("worker tools = %v, want %v", names, want)
	}
	for _, name := range []string{"kb_search", "kb_get"} {
		if slices.Contains(names, name) {
			t.Errorf("worker tools include %s", name)
		}
	}
	system, _ := body["system"].([]any)
	if len(system) != 2 {
		t.Errorf("worker system blocks = %d, want the base and the worker prompt", len(system))
	} else if text := fmt.Sprint(system[1].(map[string]any)["text"]); !strings.HasPrefix(text, prompts.Worker) {
		t.Errorf("worker system extra = %q", text)
	}
	threadID, err := serverops.WorkerThreadID(context.Background(), d.Read)
	if err != nil || threadID == 0 {
		t.Fatalf("worker thread = %d, %v", threadID, err)
	}
	findingID, _, _ := workerFinding(t, d, "node-a:up")
	var severity, findingTitle, findingBody string
	var marks int64
	if err := d.Read.QueryRow("SELECT severity, title, body, marks FROM ops_findings WHERE id = ?", findingID).
		Scan(&severity, &findingTitle, &findingBody, &marks); err != nil || severity != "warning" || findingTitle != "Host node-a is down" || marks != 1 {
		t.Errorf("finding = %q %q %q %d, %v", severity, findingTitle, findingBody, marks, err)
	}
	var runs int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM runs WHERE thread_id = ? AND kind = 'worker'", threadID).Scan(&runs); err != nil || runs != 1 {
		t.Errorf("worker runs = %d, %v, want one", runs, err)
	}
	ctx := config.With(settings.With(context.Background(), f.settings), f.state)
	result, err := attention.Query(ctx, d.Read, float64(time.Now().Unix()), serverops.AttentionSource(fixtureEnv(f)))
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, item := range result.Items {
		if item.Kind == "ops_finding" && strings.Contains(item.Title, "node-a") {
			found = true
		}
	}
	if !found {
		t.Errorf("attention has no ops finding: %+v", result.Items)
	}
	quiet := len(api.Requests())
	runOpsSampler(t, f, d, samplerFinished(t, d))
	if len(api.Requests()) != quiet {
		t.Errorf("an identical pass made %d model calls", len(api.Requests())-quiet)
	}
	fake.setUp(1)
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("recovered")))
	runOpsSampler(t, f, d, samplerFinished(t, d))
	waitForOps(t, d, "the finding resolved", func() bool {
		_, resolved, found := workerFinding(t, d, "node-a:up")
		return found && resolved
	})
}

func TestWorkerFindingRefusesAnUnchangedFact(t *testing.T) {
	fake := &fakeOpsGrafana{up: 1}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	dir, api, f := moduleFixture(t, flagsExcept("", ""))
	setEnv(t, settingEnv(grafanatools.KeyURL), srv.URL)
	setEnv(t, hostEnv("grafana", grafanatools.KeyMetricsUID), "metrics")
	setEnv(t, settingEnv(serverops.KeyHosts), "node-a")
	warm(t, api)
	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
	defer func() { _ = d.Close() }()
	runOpsSampler(t, f, d, samplesAtLeast(t, d, 6))
	before := len(api.Requests())
	fake.setUp(0)
	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("f1", "ops_finding", map[string]any{
		"fact": "node-a:cpu_pct", "severity": "warning", "title": "CPU high", "body": "cpu_pct changed.",
	})))
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("done")))
	finished := samplerFinished(t, d)
	runOpsSampler(t, f, d, func() bool {
		return finished() && len(api.Requests()) >= before+2
	})
	result := toolResultFor(t, api.Requests()[before+1], "f1")
	if result["is_error"] != true {
		t.Errorf("ops_finding on an unchanged fact = %v, want an error", result)
	}
	if _, _, found := workerFinding(t, d, "node-a:cpu_pct"); found {
		t.Errorf("ops_finding wrote a finding for an unchanged fact")
	}
}

func TestWorkerStatusLeavesTheChatUnmarked(t *testing.T) {
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	warm(t, api)
	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
	defer func() { _ = d.Close() }()
	now := float64(time.Now().Unix())
	res, err := d.Write.Exec("INSERT INTO ops_findings (fact, severity, title, body, marks, created_at) "+
		"VALUES ('node-a:up', 'warning', 'Host node-a is down', 'up is down', 1, ?)", now)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("w1", "worker_status", map[string]any{})))
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("status")))
	if ran := runApp(t, "agent", "run", "--text", "ops status"); ran.code != cli.ExitOK {
		t.Fatalf("status chat: %d %s", ran.code, ran.stderr)
	}
	statusThread := latestThread(t, dir)
	if marks := threadMarkSet(t, dir, statusThread); marks != 0 {
		t.Errorf("thread marks after worker_status = %d, want none", marks)
	}
	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("k1", "worker_finding", map[string]any{"id": id})))
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("finding")))
	if ran := runApp(t, "agent", "run", "--text", "read the finding"); ran.code != cli.ExitOK {
		t.Fatalf("worker_finding chat: %d %s", ran.code, ran.stderr)
	}
	getThread := latestThread(t, dir)
	if marks := threadMarkSet(t, dir, getThread); marks&int64(tool.Untrusted) == 0 {
		t.Errorf("thread marks after worker_finding = %d, want untrusted", marks)
	}
}

func TestOpsWorkerStopsAtItsMonthlyBudget(t *testing.T) {
	fake := &fakeOpsGrafana{up: 1}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	dir, api, f := moduleFixture(t, flagsExcept("", ""))
	setEnv(t, settingEnv(grafanatools.KeyURL), srv.URL)
	setEnv(t, hostEnv("grafana", grafanatools.KeyMetricsUID), "metrics")
	setEnv(t, settingEnv(serverops.KeyHosts), "node-a")
	setEnv(t, settingEnv(serverops.KeyWorkerMonthlyUSD), "0")
	warm(t, api)
	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
	defer func() { _ = d.Close() }()
	runOpsSampler(t, f, d, samplesAtLeast(t, d, 6))
	before := len(api.Requests())
	fake.setUp(0)
	runOpsSampler(t, f, d, samplerFinished(t, d))
	if len(api.Requests()) != before {
		t.Errorf("a spent budget still made %d model calls", len(api.Requests())-before)
	}
	ctx := config.With(settings.With(context.Background(), f.settings), f.state)
	result, err := attention.Query(ctx, d.Read, float64(time.Now().Unix()), serverops.AttentionSource(fixtureEnv(f)))
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, item := range result.Items {
		if item.Kind == "ops_budget" {
			found = true
		}
	}
	if !found {
		t.Errorf("attention has no budget item: %+v", result.Items)
	}
}

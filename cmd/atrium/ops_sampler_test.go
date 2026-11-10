package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	grafanatools "github.com/jeiang/atrium/internal/assistant/tools/grafana"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/kb"
	"github.com/jeiang/atrium/internal/serverops"
)

type opsGrafanaRequest struct {
	Method string
	Path   string
}

type fakeOpsGrafana struct {
	mu       sync.Mutex
	requests []opsGrafanaRequest
	alerts   []map[string]any
	up       float64
}

func (f *fakeOpsGrafana) setUp(value float64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.up = value
}

func (f *fakeOpsGrafana) upValue() float64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.up
}

func (f *fakeOpsGrafana) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.requests = append(f.requests, opsGrafanaRequest{r.Method, r.URL.Path})
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/api/ds/query":
		var body struct {
			Queries []struct {
				Expr string `json:"expr"`
			} `json:"queries"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		expr := ""
		if len(body.Queries) > 0 {
			expr = body.Queries[0].Expr
		}
		value := 0.0
		switch {
		case strings.Contains(expr, "node_cpu_seconds_total"):
			value = 71.2
		case strings.Contains(expr, "node_memory_MemAvailable_bytes"):
			value = 61.5
		case strings.Contains(expr, "node_filesystem_avail_bytes"):
			value = 40
		case strings.Contains(expr, "node_systemd_unit_state"):
			value = 0
		default:
			value = f.upValue()
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"results": map[string]any{"A": map[string]any{"frames": []any{
			map[string]any{
				"schema": map[string]any{"fields": []any{
					map[string]any{"type": "time"},
					map[string]any{"type": "number", "labels": map[string]any{"instance": "node-a:9100"}},
				}},
				"data": map[string]any{"values": []any{[]any{1700000000}, []any{value}}},
			},
		}}}})
	case r.Method == http.MethodGet && r.URL.Path == "/api/prometheus/grafana/api/v1/alerts":
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"alerts": f.alerts}})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *fakeOpsGrafana) seen() []opsGrafanaRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.requests)
}

func runOpsSampler(t *testing.T, f *fixture, d *db.DB, wait func() bool) {
	t.Helper()
	start := func() (context.CancelFunc, chan appRun) {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan appRun, 1)
		go func() { done <- runAppContext(t, f, ctx, "agent", "scheduler") }()
		return cancel, done
	}
	cancel, done := start()
	for range 50 {
		var n int
		if err := d.Read.QueryRow("SELECT COUNT(*) FROM jobs WHERE name = ?", serveropsJobName).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n > 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	cancel()
	<-done
	now := float64(time.Now().Unix())
	if _, err := d.Write.Exec("UPDATE jobs SET state = 'active', next_due = ?, lease_owner = NULL, lease_until = NULL WHERE name = ?", now-1, serveropsJobName); err != nil {
		t.Fatal(err)
	}
	cancel, done = start()
	for deadline := time.Now().Add(180 * time.Second); !wait(); {
		if time.Now().After(deadline) {
			t.Fatal("the sampler did not finish")
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	<-done
}

func samplesExist(t *testing.T, d *db.DB) func() bool {
	return func() bool {
		var n int
		if err := d.Read.QueryRow("SELECT COUNT(*) FROM ops_samples").Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n >= 6
	}
}

const serveropsJobName = "ops-sampler"

func TestSchemaVersionEightMigratesToOpsSamples(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "atrium.db")
	if code, _, stderr := runIn(t, dir, "settings", "list"); code != cli.ExitOK {
		t.Fatalf("settings list: exit %d %s", code, stderr)
	}
	d := mustConnect(t, path)
	if _, err := d.Write.Exec("DROP TABLE ops_samples"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.Exec("DELETE FROM schema_migrations WHERE version = 9"); err != nil {
		t.Fatal(err)
	}
	_ = d.Close()
	if code, _, stderr := runIn(t, dir, "settings", "list"); code != cli.ExitOK {
		t.Fatalf("settings list after removing migration 9: exit %d %s", code, stderr)
	}
	d = mustConnect(t, path)
	var n int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'ops_samples'").Scan(&n); err != nil || n != 1 {
		t.Errorf("ops_samples after migration = %d, %v, want one table", n, err)
	}
	var version int
	if err := d.Read.QueryRow("SELECT MAX(version) FROM schema_migrations").Scan(&version); err != nil || version != db.LatestSchemaVersion() || version != 9 {
		t.Errorf("schema version = %d, %v, want %d", version, err, db.LatestSchemaVersion())
	}
	_ = d.Close()
	fresh := t.TempDir()
	if code, _, stderr := runIn(t, fresh, "settings", "list"); code != cli.ExitOK {
		t.Fatalf("settings list on a fresh database: exit %d %s", code, stderr)
	}
	migrated, created := schemaDump(t, path), schemaDump(t, filepath.Join(fresh, "atrium.db"))
	if !slices.Equal(migrated, created) {
		for _, line := range migrated {
			if !slices.Contains(created, line) {
				t.Errorf("only in the migrated schema: %s", line)
			}
		}
	}
}

func TestOpsSamplerWritesTypedFactsWithoutAModel(t *testing.T) {
	fake := &fakeOpsGrafana{alerts: []map[string]any{
		{"state": "firing", "labels": map[string]any{"alertname": "HighCPU", "instance": "node-a:9100"}},
		{"state": "inactive", "labels": map[string]any{"alertname": "Ignored", "instance": "node-a:9100"}},
	}}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	dir, api, f := moduleFixture(t, flagsExcept("", ""))
	setEnv(t, settingEnv(grafanatools.KeyURL), srv.URL)
	setEnv(t, settingEnv(grafanatools.KeyMetricsUID), "metrics")
	setEnv(t, settingEnv(serverops.KeyHosts), "node-a")
	warm(t, api)
	requests := len(api.Requests())
	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
	defer func() { _ = d.Close() }()
	runOpsSampler(t, f, d, samplesExist(t, d))
	if len(api.Requests()) != requests {
		t.Fatalf("the sampler made a model call")
	}
	var samples int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM ops_samples").Scan(&samples); err != nil || samples != 6 {
		t.Errorf("samples = %d, %v, want 6", samples, err)
	}
	var value float64
	var state *string
	if err := d.Read.QueryRow("SELECT value, state FROM ops_samples WHERE metric = 'up'").Scan(&value, &state); err != nil || value != 0 || state == nil || *state != "down" {
		t.Errorf("up sample = %v %v, %v, want 0 down", value, state, err)
	}
	if err := d.Read.QueryRow("SELECT value FROM ops_samples WHERE metric = 'cpu_pct'").Scan(&value); err != nil || value != 71.2 {
		t.Errorf("cpu sample = %v, %v, want 71.2", value, err)
	}
	var detail string
	if err := d.Read.QueryRow("SELECT detail FROM ops_samples WHERE metric = 'alerts_firing'").Scan(&detail); err != nil || !strings.Contains(detail, "HighCPU") || strings.Contains(detail, "Ignored") {
		t.Errorf("alerts detail = %q, %v", detail, err)
	}
	var facts int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM kb_records WHERE scope = 'ops' AND marks = 0 AND writer = 'worker'").Scan(&facts); err != nil || facts != 6 {
		t.Errorf("ops facts = %d, %v, want 6 unmarked worker facts", facts, err)
	}
	now := float64(time.Now().Unix())
	var expired int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM kb_records WHERE scope = 'ops' AND expires_at <= ?", now).Scan(&expired); err != nil || expired != 0 {
		t.Errorf("ops facts with no 24h expiry = %d, %v", expired, err)
	}
	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("k1", "kb_search", map[string]any{"query": "node-a", "scope": "ops"})))
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("found")))
	if ran := runApp(t, "agent", "run", "--text", "what is the host doing"); ran.code != cli.ExitOK {
		t.Fatalf("chat run: %d %s", ran.code, ran.stderr)
	}
	thread := latestThread(t, dir)
	if marks := threadMarkSet(t, dir, thread); marks != 0 {
		t.Errorf("chat thread marks after an ops search = %d, want none", marks)
	}
	last := api.Requests()[len(api.Requests())-1]
	result := toolResultFor(t, last, "k1")
	if result["is_error"] == true || !strings.Contains(fmt.Sprint(result["content"]), "node-a") {
		t.Errorf("kb_search scope ops returned %v", result)
	}
	if _, err := d.Write.Exec("INSERT INTO ops_samples (ts, host, metric, value) VALUES (?, 'node-a', 'cpu_pct', 1)", now-15*24*3600); err != nil {
		t.Fatal(err)
	}
	runOpsSampler(t, f, d, func() bool {
		var old int
		if err := d.Read.QueryRow("SELECT COUNT(*) FROM ops_samples WHERE ts < ?", now-14*24*3600).Scan(&old); err != nil {
			t.Fatal(err)
		}
		return old == 0
	})
	future, err := kb.Search(context.Background(), d.Read, kb.SearchFilter{Query: "node-a", Scope: kb.ScopeOps, Limit: 10, Now: now + 25*3600})
	if err != nil {
		t.Fatal(err)
	}
	if len(future.Hits) != 0 {
		t.Errorf("facts still visible after 24h: %d hits", len(future.Hits))
	}
}

func TestOpsQuerySettingsRefuseNonQueryValues(t *testing.T) {
	dir := t.TempDir()
	if code, _, stderr := runIn(t, dir, "settings", "list"); code != cli.ExitOK {
		t.Fatalf("settings list: exit %d %s", code, stderr)
	}
	bad := []struct{ key, value string }{
		{"ops_query_up", `up{`},
		{"ops_query_up", `up{job="node"}`},
		{"ops_query_up", "up{job=\"node\"}\nnode_systemd_unit_state"},
		{"ops_label_selector", `job="node"}`},
		{"ops_hosts", `node"}`},
	}
	for _, c := range bad {
		if code, _, _ := runIn(t, dir, "settings", "set", c.key, c.value); code == cli.ExitOK {
			t.Errorf("settings set %s %q was accepted", c.key, c.value)
		}
	}
	good := []struct{ key, value string }{
		{"ops_label_selector", `job="node"`},
		{"ops_query_up", `up{{{selector}},instance=~"{{host}}(:.*)?"}`},
		{"ops_hosts", "node-a"},
	}
	for _, c := range good {
		if code, _, stderr := runIn(t, dir, "settings", "set", c.key, c.value); code != cli.ExitOK {
			t.Errorf("settings set %s %q: exit %d %s", c.key, c.value, code, stderr)
		}
	}
}

func TestOpsSamplerNeverWritesToGrafana(t *testing.T) {
	fake := &fakeOpsGrafana{}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	dir, api, f := moduleFixture(t, flagsExcept("", ""))
	setEnv(t, settingEnv(grafanatools.KeyURL), srv.URL)
	setEnv(t, settingEnv(grafanatools.KeyMetricsUID), "metrics")
	setEnv(t, settingEnv(serverops.KeyHosts), "node-a")
	warm(t, api)
	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
	defer func() { _ = d.Close() }()
	runOpsSampler(t, f, d, samplesExist(t, d))
	for _, request := range fake.seen() {
		if request.Method == http.MethodGet {
			continue
		}
		if request.Method == http.MethodPost && request.Path == "/api/ds/query" {
			continue
		}
		t.Errorf("the sampler sent %s %s", request.Method, request.Path)
	}
}

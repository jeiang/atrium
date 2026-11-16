package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/cli"
)

func lifecycleRow(t *testing.T, dir, id string) (enabled bool, state, reason string) {
	t.Helper()
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
	if err := d.Read.QueryRow("SELECT enabled, state, reason FROM connections WHERE id = ?", id).Scan(&enabled, &state, &reason); err != nil {
		t.Fatalf("connection %s: %v", id, err)
	}
	return enabled, state, reason
}

func lifecycleCounts(t *testing.T, dir string) (rows, creds int) {
	t.Helper()
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
	if err := d.Read.QueryRow("SELECT (SELECT COUNT(*) FROM connections), (SELECT COUNT(*) FROM connection_credentials)").Scan(&rows, &creds); err != nil {
		t.Fatal(err)
	}
	return rows, creds
}

func seedOperatorConnection(t *testing.T, dir, id, kind, label, config, capability, state string, credential map[string]string) {
	t.Helper()
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
	now := float64(time.Now().Unix())
	old := now - 3*86400
	if _, err := d.Write.Exec("INSERT INTO connections(id, kind, label, config_source, config_json, enabled, state, checked_at, created_at, updated_at) "+
		"VALUES (?, ?, ?, 'operator', ?, 1, ?, ?, ?, ?)", id, kind, label, config, state, old, old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.Exec("INSERT INTO connection_capabilities(connection_id, name) VALUES (?, ?)", id, capability); err != nil {
		t.Fatal(err)
	}
	for slot, value := range credential {
		if _, err := d.Write.Exec("INSERT INTO connection_credentials(connection_id, slot, source, value, set_at, set_by) VALUES (?, ?, 'database', ?, ?, 'operator')",
			id, slot, value, old); err != nil {
			t.Fatal(err)
		}
	}
}

func countingServer(t *testing.T, body string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func setFlag(t *testing.T, s *apiSite, token, name, value string) {
	t.Helper()
	if got := s.do(http.MethodPut, "/api/v1/settings/"+name+"_enabled", token, map[string]any{"value": value}); got.status != http.StatusOK {
		t.Fatalf("set %s_enabled=%s: %d %.400s", name, value, got.status, got.body)
	}
}

func TestBuiltinConnectionsFollowTheirAppsWhileOperatorRowsOnlyRest(t *testing.T) {
	requireCipher(t)
	flags := flagsExcept("media", "false")
	flags["mail"] = "false"
	dir, _, _ := moduleFixture(t, flags)
	eztv, eztvHits := countingServer(t, `{"torrents":[{"id":1}]}`)
	ship24, ship24Hits := countingServer(t, `{"data":{"trackers":[]}}`)
	seedOperatorConnection(t, dir, "service/eztv", "service/eztv", "EZTV", `{"url":"`+eztv.URL+`/api/get-torrents"}`, "indexer", "ok", nil)
	seedOperatorConnection(t, dir, "service/ship24", "service/ship24", "Ship24", `{"url":"`+ship24.URL+`/public/v1"}`, "api", "failing",
		map[string]string{"api_key": "ship24-secret-key"})

	site := &connectionSite{t: t, dir: dir}
	site.start()
	defer site.stop()
	token := localAPIToken(t, "lifecycle")
	api := &apiSite{t: t, base: site.base}
	rows, creds := lifecycleCounts(t, dir)

	waitFor(t, func() bool {
		enabled, state, reason := lifecycleRow(t, dir, "service/eztv")
		return !enabled && state == "disabled" && reason == "its app is off"
	})
	if enabled, _, reason := lifecycleRow(t, dir, "service/weather"); !enabled || reason != "" {
		t.Fatalf("weather with the assistant on: enabled %v reason %q", enabled, reason)
	}
	shipView := apiData(t, api.do(http.MethodGet, "/api/v1/connections/service/ship24", token, nil), http.StatusOK)
	if shipView["idle"] != "its app is off" || shipView["enabled"] != true || shipView["state"] != "failing" {
		t.Fatalf("ship24 with mail off: %v", shipView)
	}
	if enabled, state, _ := lifecycleRow(t, dir, "service/ship24"); !enabled || state != "failing" {
		t.Fatalf("operator ship24 row was written: enabled %v state %q", enabled, state)
	}
	if _, ok := capabilityDependencies(t, site.base, token).repair("service/ship24"); ok {
		t.Fatal("an idle connection still lists a repair")
	}
	if weather := apiData(t, api.do(http.MethodGet, "/api/v1/connections/service/weather", token, nil), http.StatusOK); weather["idle"] != nil {
		t.Fatalf("weather is idle with the assistant on: %v", weather)
	}

	setFlag(t, api, token, "media", "true")
	waitFor(t, func() bool {
		enabled, state, _ := lifecycleRow(t, dir, "service/eztv")
		return enabled && state == "ok"
	})
	if eztvHits.Load() == 0 {
		t.Fatal("the re-enabled EZTV row was not tested")
	}
	if enabled, _, reason := lifecycleRow(t, dir, "service/tmdb"); !enabled || reason == "its app is off" {
		t.Fatalf("tmdb after media came back: enabled %v reason %q", enabled, reason)
	}
	setFlag(t, api, token, "media", "false")
	waitFor(t, func() bool {
		enabled, _, reason := lifecycleRow(t, dir, "service/eztv")
		return !enabled && reason == "its app is off"
	})
	if got := ship24Hits.Load(); got != 0 {
		t.Fatalf("idle ship24 was tested %d times", got)
	}
	if got := noticeBodies(t, dir, "Ship24"); len(got) != 0 {
		t.Fatalf("idle ship24 raised notices %v", got)
	}
	if got := noticeBodies(t, dir, "EZTV"); len(got) != 0 {
		t.Fatalf("the EZTV lifecycle raised notices %v", got)
	}

	setFlag(t, api, token, "mail", "true")
	waitFor(t, func() bool { return ship24Hits.Load() > 0 && connectionState(t, dir, "service/ship24") == "ok" })
	if got := apiData(t, api.do(http.MethodGet, "/api/v1/connections/service/ship24", token, nil), http.StatusOK); got["idle"] != nil {
		t.Fatalf("ship24 with mail on is still idle: %v", got)
	}
	if r, c := lifecycleCounts(t, dir); r != rows || c != creds {
		t.Fatalf("rows %d credentials %d after the lifecycle, want %d %d", r, c, rows, creds)
	}
}

func TestOperatorTurnsConnectionsOnAndOffOnBothSurfaces(t *testing.T) {
	requireCipher(t)
	dir, _, _ := moduleFixture(t, flagsExcept("media", "false"))
	site := &connectionSite{t: t, dir: dir}
	site.start()
	defer site.stop()
	token := localAPIToken(t, "lifecycle")
	api := &apiSite{t: t, base: site.base}
	browser := newWebClient(t, site.base, nil)
	browser.session("local")
	rows, creds := lifecycleCounts(t, dir)

	off := apiData(t, browser.do(http.MethodPost, "/web/v1/connections/service/weather/disable", nil), http.StatusOK)
	if off["enabled"] != false || off["state"] != "disabled" || off["reason"] != "turned off by the operator" {
		t.Fatalf("disable over /web/v1: %v", off)
	}
	again := apiData(t, api.do(http.MethodPost, "/api/v1/connections/service/weather/disable", token, nil), http.StatusOK)
	if fmt.Sprint(again) != fmt.Sprint(off) {
		t.Fatalf("a second disable changed the row: %v then %v", off, again)
	}
	on := apiData(t, api.do(http.MethodPost, "/api/v1/connections/service/weather/enable", token, nil), http.StatusOK)
	if on["enabled"] != true || on["reason"] != "" {
		t.Fatalf("enable over /api/v1: %v", on)
	}
	expectAPI(t, api.do(http.MethodPost, "/api/v1/connections/service/anthropic/disable", token, nil), http.StatusConflict, "refused")
	waitFor(t, func() bool {
		_, _, reason := lifecycleRow(t, dir, "service/eztv")
		return reason == "its app is off"
	})
	expectAPI(t, browser.do(http.MethodPost, "/web/v1/connections/service/eztv/enable", nil), http.StatusConflict, "refused")
	expectAPI(t, api.do(http.MethodPost, "/api/v1/connections/service/nothing/enable", token, nil), http.StatusNotFound, "not_found")
	if r, c := lifecycleCounts(t, dir); r != rows || c != creds {
		t.Fatalf("rows %d credentials %d after enable and disable, want %d %d", r, c, rows, creds)
	}
}

func TestDependencyToolsReportNeedsAndRecheckWithinLimits(t *testing.T) {
	requireCipher(t)
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	ship24, ship24Hits := countingServer(t, `{"data":{"trackers":[]}}`)
	seedOperatorConnection(t, dir, "service/ship24", "service/ship24", "Ship24", `{"url":"`+ship24.URL+`/public/v1"}`, "api", "failing",
		map[string]string{"api_key": "ship24-secret-key"})
	if ran := runApp(t, "settings", "list"); ran.code != cli.ExitOK {
		t.Fatalf("settings list: %d %s", ran.code, ran.stderr)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
	if _, err := d.Write.Exec("UPDATE connections SET enabled = 0, state = 'disabled', reason = 'turned off by the operator' WHERE id = 'service/weather'"); err != nil {
		t.Fatal(err)
	}
	results := func() string {
		t.Helper()
		var out string
		if err := d.Read.QueryRow("SELECT group_concat(content_json) FROM thread_messages WHERE thread_id = (SELECT MAX(id) FROM threads) AND role = 'user'").Scan(&out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	run := func(kind string, calls ...anthropictest.Response) appRun {
		t.Helper()
		for _, c := range calls {
			api.Push(c)
		}
		api.Push(anthropictest.Reply("end_turn", anthropictest.Text("ok")))
		ran := runApp(t, "agent", "run", "--kind", kind, "--text", "how are the connections")
		if ran.code != cli.ExitOK {
			t.Fatalf("%s run: %d %s", kind, ran.code, ran.stderr)
		}
		return ran
	}
	call := func(id, name string, args map[string]any) anthropictest.Response {
		return anthropictest.Reply("tool_use", anthropictest.ToolUse(id, name, args))
	}

	chat := run("chat", call("s1", "dependencies_status", map[string]any{}))
	got := results()
	if !strings.Contains(got, `\"connection\":\"service/ship24\"`) || !strings.Contains(got, `\"repair\":\"connection:service/ship24:`) ||
		strings.Contains(got, "ship24-secret-key") {
		t.Fatalf("dependencies_status: %s", got)
	}
	names := offeredTools(t, api, chat)
	if !containsName(names, "dependencies_status") || !containsName(names, "dependency_recheck") {
		t.Fatalf("chat tools %v", names)
	}
	for _, name := range names {
		if strings.Contains(name, "enable") || strings.Contains(name, "disable") {
			t.Fatalf("an agent tool can turn connections on or off: %s", name)
		}
	}

	run("chat",
		call("r1", "dependency_recheck", map[string]any{"connection": "service/ship24"}),
		call("r2", "dependency_recheck", map[string]any{"connection": "service/ship24"}),
		call("r3", "dependency_recheck", map[string]any{"connection": "service/weather"}))
	got = results()
	if !strings.Contains(got, `\"state\":\"ok\"`) || !strings.Contains(got, "was rechecked recently") ||
		!strings.Contains(got, "service/weather is disabled") || ship24Hits.Load() != 1 {
		t.Fatalf("dependency_recheck (%d ship24 calls): %s", ship24Hits.Load(), got)
	}
	if state := connectionState(t, dir, "service/ship24"); state != "ok" {
		t.Fatalf("ship24 after a recheck: %s", state)
	}

	nightly := run("nightly", call("n1", "dependency_recheck", map[string]any{"connection": "service/ship24"}))
	if names := offeredTools(t, api, nightly); containsName(names, "dependency_recheck") || !containsName(names, "dependencies_status") {
		t.Fatalf("nightly tools %v", names)
	}
	if got := results(); !strings.Contains(got, `"is_error":true`) || ship24Hits.Load() != 1 {
		t.Fatalf("nightly recheck (%d ship24 calls): %s", ship24Hits.Load(), got)
	}
}

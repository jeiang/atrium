package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/apps/library"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/unit"
)

func appEntryOf(t *testing.T, data map[string]any, id string) map[string]any {
	t.Helper()
	apps, _ := data["apps"].([]any)
	for _, raw := range apps {
		if entry, _ := raw.(map[string]any); entry["id"] == id {
			return entry
		}
	}
	t.Fatalf("app %s is not in the manifest: %v", id, data)
	return nil
}

func appIDs(data map[string]any) []string {
	var ids []string
	apps, _ := data["apps"].([]any)
	for _, raw := range apps {
		entry, _ := raw.(map[string]any)
		id, _ := entry["id"].(string)
		ids = append(ids, id)
	}
	return ids
}

func storedFlag(t *testing.T, dir, name string) (string, bool) {
	t.Helper()
	var value string
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
	defer func() { _ = d.Close() }()
	if err := d.Read.QueryRow("SELECT value FROM settings WHERE key = ?", module.FlagKey(name)).Scan(&value); err != nil {
		return "", false
	}
	return value, true
}

func lastRequestJSON(t *testing.T, api *modelLog) string {
	t.Helper()
	requests := api.Requests()
	if len(requests) == 0 {
		t.Fatal("no model request was sent")
	}
	raw, err := json.Marshal(requests[len(requests)-1].JSON(t))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestOperatorSwitchesAppsFromTheManifest(t *testing.T) {
	t.Parallel()
	requireCipher(t)
	dir, model, f := moduleFixture(t, flagsExcept("", ""))
	raw, err := os.ReadFile(defaultHostConfig)
	if err != nil {
		t.Fatal(err)
	}
	host := filepath.Join(dir, "apps-host.toml")
	if err := os.WriteFile(host, append(raw, []byte("\n[core]\ncalendar_enabled = true\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	setEnv(t, "ATRIUM_HOST_CONFIG", host)

	site := &connectionSite{t: t, dir: dir}
	site.start()
	defer func() { site.stop() }()
	token := localAPIToken(t, "apps")
	api := &apiSite{t: t, base: site.base}
	browser := newWebClient(t, site.base, nil)
	browser.session("local")
	d := asstDB(t, dir)
	rows, creds := lifecycleCounts(t, dir)
	audits := func() int {
		return asstCount(t, d, "SELECT COUNT(*) FROM settings_audit WHERE key = ?", module.FlagKey(unit.Media))
	}
	before := audits()

	listed := apiData(t, api.do(http.MethodGet, "/api/v1/apps", token, nil), http.StatusOK)
	if ids := appIDs(listed); !slices.Equal(ids, []string{"calendar", "grafana", "mail", "media", "packages", "torrents", "weather"}) {
		t.Fatalf("manifest apps = %v, want only the seven apps", ids)
	}
	if web := apiData(t, browser.do(http.MethodGet, "/web/v1/apps", nil), http.StatusOK); !slices.Equal(appIDs(web), appIDs(listed)) {
		t.Fatalf("the web manifest differs: %v", appIDs(web))
	}
	mediaApp := appEntryOf(t, listed, "media")
	if mediaApp["enabled"] != true || mediaApp["state"] != "enabled" || mediaApp["switchable"] != true || mediaApp["name"] != "Library" {
		t.Fatalf("media entry: %v", mediaApp)
	}
	if nav, _ := json.Marshal(mediaApp["nav"]); !strings.Contains(string(nav), "/apps/media/channels") {
		t.Fatalf("media nav: %s", nav)
	}
	if needs, _ := json.Marshal(mediaApp["needs"]); !strings.Contains(string(needs), `"module":"media"`) {
		t.Fatalf("media needs: %s", needs)
	}
	if grafana := appEntryOf(t, listed, "grafana"); !strings.Contains(mustJSON(t, grafana["depends"]), `"id":"assistant"`) {
		t.Fatalf("grafana depends: %v", grafana)
	}
	if !strings.Contains(mustJSON(t, mediaApp["depends"]), `"id":"torrents"`) {
		t.Fatalf("media depends: %v", mediaApp)
	}
	if torrentsApp := appEntryOf(t, listed, "torrents"); torrentsApp["enabled"] != true || !strings.Contains(mustJSON(t, torrentsApp["dependents"]), `"id":"media"`) {
		t.Fatalf("torrents entry: %v", torrentsApp)
	}
	calendar := appEntryOf(t, listed, "calendar")
	if calendar["switchable"] != false || !strings.Contains(calendar["pin_source"].(string), "host") {
		t.Fatalf("pinned calendar: %v", calendar)
	}
	revision, _ := mediaApp["revision"].(string)

	expectAPI(t, api.do(http.MethodPut, "/api/v1/apps/assistant/enabled", token, map[string]any{"enabled": false}), http.StatusConflict, "core_unit")
	expectAPI(t, api.do(http.MethodPut, "/api/v1/apps/nothing/enabled", token, map[string]any{"enabled": false}), http.StatusNotFound, "not_found")
	expectAPI(t, api.do(http.MethodPut, "/api/v1/apps/calendar/enabled", token, map[string]any{"enabled": false}), http.StatusConflict, "pinned")
	expectAPI(t, api.do(http.MethodPut, "/api/v1/apps/media/enabled", token, map[string]any{}), http.StatusUnprocessableEntity, "invalid")
	stale := api.do(http.MethodPut, "/api/v1/apps/media/enabled", token, map[string]any{"enabled": false}, "If-Match", "stale")
	expectAPI(t, stale, http.StatusPreconditionFailed, "precondition_failed")
	if got := stale.body; !strings.Contains(got, `"revision"`) {
		t.Fatalf("a stale switch did not carry the revision: %s", got)
	}
	if v, ok := storedFlag(t, dir, unit.Media); !ok || v != "true" {
		t.Fatalf("a refused switch wrote media_enabled = %q", v)
	}

	if got := runApp(t, "agent", "run", "--text", "hello", "--kind", "chat"); got.code != cli.ExitOK {
		t.Fatalf("agent run: exit %d %s", got.code, got.stderr)
	}
	model.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("u1", "apps_switch", map[string]any{"id": "media", "enabled": false})), asstReply("done"))
	ran := runApp(t, "agent", "run", "--text", "turn media off", "--kind", "chat")
	if names := offeredTools(t, model, ran); slices.ContainsFunc(names, func(n string) bool { return strings.Contains(n, "apps") || strings.Contains(n, "settings") }) {
		t.Fatalf("the agent is offered an app or settings switch: %v", names)
	}
	if body := lastRequestJSON(t, model); !strings.Contains(body, `"is_error":true`) {
		t.Fatalf("the agent's switch call was not refused: %.600s", body)
	}
	if v, _ := storedFlag(t, dir, unit.Media); v != "true" || audits() != before {
		t.Fatalf("the agent changed media_enabled to %q", v)
	}

	expectAPI(t, api.do(http.MethodPut, "/api/v1/apps/torrents/enabled", token, map[string]any{"enabled": false}), http.StatusConflict, "dependent_on")
	if v, _ := storedFlag(t, dir, unit.Torrents); v != "true" {
		t.Fatalf("a refused switch turned torrents off: %q", v)
	}
	off := apiData(t, api.do(http.MethodPut, "/api/v1/apps/media/enabled", token, map[string]any{"enabled": false}, "If-Match", revision), http.StatusOK)
	if off["enabled"] != false || off["state"] != "off" {
		t.Fatalf("switched media: %v", off)
	}
	if audits() != before+1 {
		t.Fatalf("settings audit rows for media_enabled = %d, want %d", audits(), before+1)
	}
	expectAPI(t, browser.do(http.MethodGet, "/web/v1/media/channels", nil), http.StatusNotFound, "not_found")
	if shell := browser.do(http.MethodGet, "/web/v1/shell", nil); shell.status != http.StatusOK || strings.Contains(shell.body, "/apps/media/") {
		t.Fatalf("the shell still links media: %d %.400s", shell.status, shell.body)
	}
	waitFor(t, func() bool {
		enabled, state, reason := lifecycleRow(t, dir, "service/eztv")
		return !enabled && state == "disabled" && reason == "its app is off"
	})
	model.Push(asstReply("ok"))
	ran = runApp(t, "agent", "run", "--text", "hello", "--kind", "chat")
	if slices.Contains(offeredTools(t, model, ran), "library_following") {
		t.Fatal("library_following is offered with media off")
	}
	if system := systemText(t, model.Requests()[len(model.Requests())-1]); !strings.Contains(system, "Library (media)") || strings.Contains(system, "Mail (mail)") {
		t.Fatalf("the system prompt does not name only the off apps: %.400s", system)
	}
	sweep, _, _ := schedulerJob(t, d, library.JobSweep)
	if got := api.do(http.MethodPost, "/api/v1/schedules/"+strconv.FormatInt(sweep, 10)+"/run", token, nil); got.status < 300 {
		waitFor(t, func() bool {
			status, _ := schedulerStatus(t, d, library.JobSweep)
			return status == "skipped_module"
		})
	} else if got.status != http.StatusConflict {
		t.Fatalf("run-now of the media sweep with media off: %d %.400s", got.status, got.body)
	}

	apiData(t, api.do(http.MethodPut, "/api/v1/apps/torrents/enabled", token, map[string]any{"enabled": false}), http.StatusOK)
	expectAPI(t, api.do(http.MethodPut, "/api/v1/apps/media/enabled", token, map[string]any{"enabled": true}), http.StatusConflict, "dependency_off")
	if v, _ := storedFlag(t, dir, unit.Media); v != "false" {
		t.Fatalf("a refused switch turned media on: %q", v)
	}
	apiData(t, api.do(http.MethodPut, "/api/v1/apps/torrents/enabled", token, map[string]any{"enabled": true}), http.StatusOK)
	on := browser.do(http.MethodPut, "/web/v1/apps/media/enabled", map[string]any{"enabled": true})
	if data := apiData(t, on, http.StatusOK); data["enabled"] != true || data["state"] != "enabled" {
		t.Fatalf("media back on: %v", data)
	}
	apiData(t, browser.do(http.MethodGet, "/web/v1/media/channels", nil), http.StatusOK)
	waitFor(t, func() bool {
		enabled, _, reason := lifecycleRow(t, dir, "service/eztv")
		return enabled && reason != "its app is off"
	})
	model.Push(asstReply("ok"))
	ran = runApp(t, "agent", "run", "--text", "hello", "--kind", "chat")
	if !slices.Contains(offeredTools(t, model, ran), "library_following") {
		t.Fatal("library_following is missing with media back on")
	}
	if system := systemText(t, model.Requests()[len(model.Requests())-1]); strings.Contains(system, "These apps are turned off") {
		t.Fatalf("the prompt names off apps while all are on: %.400s", system)
	}
	if r, c := lifecycleCounts(t, dir); r != rows || c != creds || !tableExists(t, dir, "media_channels") {
		t.Fatalf("switching changed rows %d->%d credentials %d->%d or dropped media_channels", rows, r, creds, c)
	}

	cmd := builtAtrium(t, "settings", "set", module.FlagKey(unit.Assistant), "false")
	for name, value := range f.env {
		cmd.Env = append(cmd.Env, name+"="+value)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("atrium settings set assistant_enabled false: %v %s", err, out)
	}
	waitFor(t, func() bool {
		enabled, _, reason := lifecycleRow(t, dir, "service/webfetch")
		return !enabled && reason == "its app is off"
	})
	waitFor(t, func() bool {
		data := apiData(t, api.do(http.MethodGet, "/api/v1/apps", token, nil), http.StatusOK)
		return appEntryOf(t, data, "grafana")["state"] != "enabled"
	})
	apiData(t, api.do(http.MethodPut, "/api/v1/apps/grafana/enabled", token, map[string]any{"enabled": false}), http.StatusOK)
	refused := api.do(http.MethodPut, "/api/v1/apps/grafana/enabled", token, map[string]any{"enabled": true})
	expectAPI(t, refused, http.StatusConflict, "dependency_off")
	if v, _ := storedFlag(t, dir, "grafana"); v != "false" {
		t.Fatalf("a refused switch turned grafana on: %q", v)
	}
}

func TestAppFlagsKeepTheirStoredStates(t *testing.T) {
	t.Parallel()
	flags := flagsExcept(unit.Media, "false")
	delete(flags, unit.Calendar)
	dir, _, _ := moduleFixture(t, flags)
	d := asstDB(t, dir)
	audits := asstCount(t, d, "SELECT COUNT(*) FROM settings_audit WHERE key LIKE '%_enabled'")
	if got := runApp(t, "settings", "list"); got.code != cli.ExitOK {
		t.Fatalf("settings list: exit %d %s", got.code, got.stderr)
	}
	site := &connectionSite{t: t, dir: dir}
	site.start()
	defer func() { site.stop() }()
	data := apiData(t, (&apiSite{t: t, base: site.base}).do(http.MethodGet, "/api/v1/apps", localAPIToken(t, "apps"), nil), http.StatusOK)
	for id, want := range map[string]bool{unit.Mail: true, unit.Media: false, unit.Calendar: false} {
		if got := appEntryOf(t, data, id)["enabled"]; got != want {
			t.Errorf("%s enabled = %v, want %v", id, got, want)
		}
	}
	if _, ok := storedFlag(t, dir, unit.Calendar); ok {
		t.Error("an absent calendar flag row was written")
	}
	if got := asstCount(t, d, "SELECT COUNT(*) FROM settings_audit WHERE key LIKE '%_enabled'"); got != audits {
		t.Errorf("starting wrote %d flag audit rows", got-audits)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

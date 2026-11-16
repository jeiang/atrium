package main

import (
	"fmt"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/core/coremodule"
	"github.com/jeiang/atrium/internal/notify"
	"github.com/jeiang/atrium/internal/settings"
)

var retiredToConstants = []settings.RetiredKey{
	{Module: "assistant", Key: "cache_ttl_chat"},
	{Module: "assistant", Key: "prompt_subagent"},
	{Module: "mail", Key: "mail_ship24_monthly_quota"},
	{Module: "mail", Key: "watches_carrier_domains"},
	{Module: "memory", Key: "memory_effort"},
	{Module: "memory", Key: "memory_model"},
	{Module: "memory", Key: "memory_verbatim_tokens"},
	{Module: "notify", Key: "notify_nightly_cap"},
	{Module: "notify", Key: "notify_per_run_cap"},
	{Module: "web", Key: "snooze_choices"},
}

func TestRetiredSettingsAreIgnoredAndTheirConstantsHold(t *testing.T) {
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	start := warm(t, api)
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
	for _, r := range retiredToConstants {
		if _, ok := settings.RetiredAt(r.Module, r.Key); !ok {
			t.Fatalf("%s.%s is not on the retired list", r.Module, r.Key)
		}
		if _, err := d.Write.Exec("INSERT INTO settings (key, value) VALUES (?, '1')", r.Key); err != nil {
			t.Fatal(err)
		}
		setEnv(t, r.EnvName(), "1")
	}
	_ = d.Close()

	listed := runAppLogged(t, "settings", "list")
	if listed.code != cli.ExitOK {
		t.Fatalf("settings list with retired rows and pins: %d %s", listed.code, listed.stderr)
	}
	for _, r := range retiredToConstants {
		if strings.Contains(listed.stdout, `"`+r.Key+`"`) {
			t.Errorf("retired %s is still listed", r.Key)
		}
		if !strings.Contains(listed.stderr, r.Module+"."+r.Key) || !strings.Contains(listed.stderr, "ignoring a removed setting") {
			t.Errorf("the %s pin was not reported as ignored: %s", r.EnvName(), listed.stderr)
		}
		for _, args := range [][]string{{"get", r.Key}, {"set", r.Key, "2"}, {"unset", r.Key}} {
			got := runSettings(t, args...)
			if got.code != cli.ExitUsage || !strings.Contains(got.stderr, `unknown key "`+r.Key+`"`) {
				t.Errorf("settings %v: exit %d stderr %q, want the unknown key refusal", args, got.code, got.stderr)
			}
		}
	}

	var replies []anthropictest.Response
	for i := 1; i <= notify.TuningPerRunCap+1; i++ {
		replies = append(replies, anthropictest.Reply("tool_use", anthropictest.ToolUse(fmt.Sprintf("n%d", i), "notify", map[string]any{"title": "Check", "body": fmt.Sprintf("note %d", i)})))
	}
	api.Push(append(replies, anthropictest.Reply("end_turn", anthropictest.Text("done")))...)
	if ran := runApp(t, "agent", "run", "--text", "notify me a few times"); ran.code != cli.ExitOK {
		t.Fatalf("notify run: %d %s", ran.code, ran.stderr)
	}
	requests := api.Requests()[start:]
	last := requests[len(requests)-1]
	for i := 1; i <= notify.TuningPerRunCap; i++ {
		if result := toolResultFor(t, last, fmt.Sprintf("n%d", i)); strings.Contains(fmt.Sprint(result["content"]), "per run") {
			t.Errorf("notification %d was refused under the cap of %d: %v", i, notify.TuningPerRunCap, result)
		}
	}
	over := toolResultFor(t, last, fmt.Sprintf("n%d", notify.TuningPerRunCap+1))
	if over["is_error"] != true || !strings.Contains(fmt.Sprint(over["content"]), "at most 3 notifications per run") {
		t.Errorf("notification over the cap: %v", over)
	}
}

var offeredHere = []struct{ key, group, place, value, invalid string }{
	{"tz", "core", settings.PlaceSettingsGeneral, "Europe/Lisbon", "Mars/Olympus"},
	{"digest_notify", "assistant", settings.PlaceAssistantRoutines, "false", "sometimes"},
	{"agent_schedule_min_interval_seconds", "assistant", settings.PlaceAssistantRoutines, "7200", "5"},
}

func TestOfferedSettingsPatchOnBothSurfacesWithIfMatch(t *testing.T) {
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	s := startAPISite(t, dir, api)
	token := s.token("owner")
	browser := newWebClient(t, s.base, nil)
	browser.http = oidcBrowser(t, s.base, s.idp, "owner")
	browser.session("oidc")
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
	t.Cleanup(func() { _ = d.Close() })
	stored := func(key string) string {
		t.Helper()
		var v string
		if err := d.Read.QueryRow("SELECT COALESCE((SELECT value FROM settings WHERE key = ?), '')", key).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}

	schema := strictJSON[coremodule.SettingsSchema](t, "/settings/schema", s.do(http.MethodGet, "/api/v1/settings/schema", token, nil))
	for _, p := range schema.Pages {
		for _, f := range p.Fields {
			if f.Group == "" {
				t.Errorf("schema field %s has no placement group", f.Key)
			}
			if f.Restart != (strings.HasSuffix(f.Key, "_enabled") || f.Key == "tz") {
				t.Errorf("schema field %s restart = %v", f.Key, f.Restart)
			}
			if slices.ContainsFunc(retiredToConstants, func(r settings.RetiredKey) bool { return r.Key == f.Key }) {
				t.Errorf("schema lists retired %s", f.Key)
			}
		}
	}
	if tz := schemaField(t, schema, "tz"); !tz.Editable || !strings.Contains(tz.Help, "restart") {
		t.Errorf("tz field: %+v", tz)
	}

	surfaces := []struct {
		prefix string
		do     func(method, path string, body any, headers ...string) apiReply
	}{
		{"/api/v1", func(method, path string, body any, headers ...string) apiReply {
			return s.do(method, path, token, body, headers...)
		}},
		{"/web/v1", browser.do},
	}
	for _, tc := range offeredHere {
		field := schemaField(t, schema, tc.key)
		if field.Group != tc.place || !field.Editable {
			t.Errorf("%s schema field: %+v", tc.key, field)
		}
		listed := apiData(t, s.do(http.MethodGet, "/api/v1/settings?group="+tc.group+"&limit=200", token, nil), http.StatusOK)
		items, _ := listed["items"].([]any)
		if !slices.ContainsFunc(items, func(item any) bool { return item.(map[string]any)["key"] == tc.key }) {
			t.Errorf("GET /settings?group=%s does not list %s: %v", tc.group, tc.key, listed["items"])
		}

		for _, surface := range surfaces {
			read := surface.do(http.MethodGet, surface.prefix+"/settings?group="+tc.group+"&limit=200", nil)
			apiData(t, read, http.StatusOK)
			rev, _ := read.json["revision"].(string)
			before := stored(tc.key)
			bad := surface.do(http.MethodPatch, surface.prefix+"/settings", map[string]any{"set": map[string]any{tc.key: tc.invalid}}, "If-Match", `W/"`+rev+`"`)
			expectAPI(t, bad, http.StatusUnprocessableEntity, "invalid")
			if stored(tc.key) != before {
				t.Fatalf("%s %s=%s wrote", surface.prefix, tc.key, tc.invalid)
			}
			ok := surface.do(http.MethodPatch, surface.prefix+"/settings", map[string]any{"set": map[string]any{tc.key: tc.value}}, "If-Match", `W/"`+rev+`"`)
			if got := apiData(t, ok, http.StatusOK); fmt.Sprint(got["keys"]) != "["+tc.key+"]" {
				t.Fatalf("%s patch %s: %v", surface.prefix, tc.key, got)
			}
			if stored(tc.key) != tc.value {
				t.Fatalf("%s patch stored %s = %q", surface.prefix, tc.key, stored(tc.key))
			}
			stale := surface.do(http.MethodPatch, surface.prefix+"/settings", map[string]any{"unset": []string{tc.key}}, "If-Match", `W/"`+rev+`"`)
			expectAPI(t, stale, http.StatusPreconditionFailed, "precondition_failed")
			if stored(tc.key) != tc.value {
				t.Fatalf("%s stale patch changed %s", surface.prefix, tc.key)
			}
			fresh := surface.do(http.MethodGet, surface.prefix+"/settings?group="+tc.group+"&limit=200", nil)
			apiData(t, fresh, http.StatusOK)
			next, _ := fresh.json["revision"].(string)
			apiData(t, surface.do(http.MethodPatch, surface.prefix+"/settings", map[string]any{"unset": []string{tc.key}}, "If-Match", `W/"`+next+`"`), http.StatusOK)
		}
	}
}

func TestOfferedSettingsPinnedByTheHostFileAreReadOnly(t *testing.T) {
	dir, api, f := moduleFixture(t, flagsExcept("", ""))
	host, _ := f.lookup("ATRIUM_HOST_CONFIG")
	setEnv(t, "ATRIUM_HOST_CONFIG", hostConfigFrom(t, host, dir, "host-pinned.toml", nil,
		"\n[core]\ntz = \"Europe/Lisbon\"\n\n[assistant]\ndigest_notify = false\nagent_schedule_min_interval_seconds = 7200\n"))
	s := startAPISite(t, dir, api)
	token := s.token("owner")
	schema := strictJSON[coremodule.SettingsSchema](t, "/settings/schema", s.do(http.MethodGet, "/api/v1/settings/schema", token, nil))
	for _, tc := range offeredHere {
		field := schemaField(t, schema, tc.key)
		if field.Editable || field.Source == nil || *field.Source != "pin" || field.PinSource == "" || field.Value == nil || *field.Value != tc.value {
			t.Errorf("pinned %s: %+v", tc.key, field)
		}
		expectAPI(t, s.do(http.MethodPatch, "/api/v1/settings", token, map[string]any{"set": map[string]any{tc.key: tc.invalid}}), http.StatusConflict, "refused")
	}
}

package main

import (
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/calendar"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/module"
)

func TestDeclaredSettingGroupSavesFromItsModulePage(t *testing.T) {
	t.Parallel()
	base := startSettingsServe(t, nil)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar}
	status, _, _ := fetch(t, client, http.MethodGet, base+"/settings/assistant", nil)
	if status != http.StatusOK {
		t.Fatalf("GET /settings/assistant = %d", status)
	}
	const action = "/settings/groups/assistant/spending"
	status, _, _ = fetch(t, client, http.MethodPost, base+action, url.Values{"thread_cost_cap_usd": {"12"}, "monthly_budget_usd": {"150"}})
	if status != http.StatusOK {
		t.Fatalf("POST %s = %d", action, status)
	}
	if row := runSettings(t, "get", "thread_cost_cap_usd").row(t); row.Value == nil || *row.Value != "12" || row.Source == nil || *row.Source != "db" {
		t.Errorf("thread_cost_cap_usd = %+v, want 12 from the database", row)
	}
	if row := runSettings(t, "get", "monthly_budget_usd").row(t); row.Source != nil && *row.Source == "db" {
		t.Errorf("saving the group stored an unchanged key: %+v", row)
	}
	status, body, _ := fetch(t, client, http.MethodPost, base+action, url.Values{"thread_cost_cap_usd": {"-1"}})
	if status != http.StatusUnprocessableEntity || !strings.Contains(body, "must be greater than 0") {
		t.Errorf("POST an invalid value = %d: %.300s", status, body)
	}
	if row := runSettings(t, "get", "thread_cost_cap_usd").row(t); row.Value == nil || *row.Value != "12" {
		t.Errorf("a refused save changed thread_cost_cap_usd to %+v", row)
	}
	if status, _, _ := fetch(t, client, http.MethodPost, base+"/settings/groups/assistant/nope", url.Values{}); status != http.StatusNotFound {
		t.Errorf("POST an unknown group = %d, want 404", status)
	}
}

func TestModuleWithASectionGetsAPageAndKeepsRefusalsThere(t *testing.T) {
	t.Parallel()
	unsetEnv(t, settingEnv(calendar.AppleIDKey))
	base := startSettingsServe(t, nil)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar}
	status, _, _ := fetch(t, client, http.MethodGet, base+"/settings/calendar", nil)
	if status != http.StatusOK {
		t.Fatalf("GET /settings/calendar = %d", status)
	}
	const action = "/settings/sections/calendar"
	status, _, _ = fetch(t, client, http.MethodPost, base+action, url.Values{"default_event_minutes": {"45"}, "back": {"/settings/calendar"}})
	if status != http.StatusOK {
		t.Errorf("saving from the module page = %d", status)
	}
	status, body, _ := fetch(t, client, http.MethodPost, base+action, url.Values{"default_event_minutes": {"0"}, "back": {"/settings/calendar"}})
	if status != http.StatusUnprocessableEntity {
		t.Errorf("a refused value = %d: %.200s", status, body)
	}
	if row := runSettings(t, "get", "default_event_minutes").row(t); row.Value == nil || *row.Value != "45" {
		t.Errorf("default_event_minutes = %+v, want 45", row)
	}
}

func TestMediaLibraryGroupEditsTheLinkTTL(t *testing.T) {
	t.Parallel()
	base := startSettingsServe(t, nil)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar}
	status, body, _ := fetch(t, client, http.MethodGet, base+"/settings/media", nil)
	if status != http.StatusOK {
		t.Fatalf("GET /settings/media = %d", status)
	}
	for _, want := range []string{`data-group="media.library"`, `name="media_link_ttl_seconds"`} {
		if !strings.Contains(body, want) {
			t.Errorf("/settings/media does not render %s", want)
		}
	}
	const action = "/settings/groups/media/library"
	status, body, _ = fetch(t, client, http.MethodPost, base+action, url.Values{"media_link_ttl_seconds": {"7200"}})
	if status != http.StatusOK || !strings.Contains(body, "data-saved") {
		t.Fatalf("POST %s = %d, saved marker %v", action, status, strings.Contains(body, "data-saved"))
	}
	if row := runSettings(t, "get", "media_link_ttl_seconds").row(t); row.Value == nil || *row.Value != "7200" {
		t.Errorf("media_link_ttl_seconds = %+v, want 7200", row)
	}
	status, body, _ = fetch(t, client, http.MethodPost, base+action, url.Values{"media_link_ttl_seconds": {"30"}})
	if status != http.StatusUnprocessableEntity || !strings.Contains(body, "must be between") {
		t.Errorf("POST an invalid TTL = %d: %.300s", status, body)
	}
	if ran := runSettings(t, "set", "media_link_ttl_seconds", "3600"); ran.code != 0 {
		t.Fatalf("settings set media_link_ttl_seconds: %d %s", ran.code, ran.stderr)
	}
	if row := runSettings(t, "get", "media_link_ttl_seconds").row(t); row.Value == nil || *row.Value != "3600" {
		t.Errorf("media_link_ttl_seconds after settings set = %+v, want 3600", row)
	}
}

func TestSettingsSaveSameRefusalInCLIAndUI(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "atrium.db")
	setEnv(t, "ATRIUM_DB", path)
	setEnv(t, "ATRIUM_CACHE_DIR", filepath.Join(dir, "cache"))
	setEnv(t, "ATRIUM_PUBLIC_HOST", "")
	pinned := module.FlagKey(module.Mail)
	seedFlagsAt(t, newTestApp(t), path, allFlagsTrue())
	setEnv(t, "ATRIUM_HOST_CONFIG", writeFile(t, dir, "host.toml", "[core]\n"+pinned+" = true\n"))
	base := startServeIn(t, dir)
	const key = "thread_cost_cap_usd"
	ran := runSettings(t, "set", key, "-1")
	if ran.code != cli.ExitUsage || !strings.HasPrefix(ran.stderr, "error: "+key+" ") {
		t.Errorf("settings set %s -1 = exit %d, stderr %q; want exit 2 naming %s", key, ran.code, ran.stderr, key)
	}
	if ran := runSettings(t, "set", pinned, "false"); ran.code != cli.ExitError || !strings.Contains(ran.stderr, pinned+" is pinned") {
		t.Errorf("settings set %s false = exit %d, stderr %q; want exit 1 naming the pin", pinned, ran.code, ran.stderr)
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar}
	if status, _, _ := fetch(t, client, http.MethodGet, base+"/settings/assistant", nil); status != http.StatusOK {
		t.Fatalf("GET /settings/assistant = %d", status)
	}
	if status, _, _ := fetch(t, client, http.MethodPost, base+"/settings/groups/assistant/spending", url.Values{key: {"-1"}}); status != http.StatusUnprocessableEntity {
		t.Errorf("POST the group with %s=-1 = %d, want 422", key, status)
	}
	if status, _, _ := fetch(t, client, http.MethodPost, base+"/settings/groups/assistant/spending", url.Values{pinned: {"false"}}); status >= http.StatusBadRequest {
		t.Errorf("POST the group with the pinned %s = %d", pinned, status)
	}
	d := mustConnect(t, path, keyFor(t))
	for k, v := range map[string]string{key: "-1", pinned: "false"} {
		var changes int
		if err := d.Read.QueryRow("SELECT COUNT(*) FROM settings_audit WHERE key = ? AND COALESCE(new_value, '') = ?", k, v).Scan(&changes); err != nil {
			t.Fatal(err)
		}
		if changes != 0 {
			t.Errorf("refused saves of %s=%s wrote %d settings_audit rows", k, v, changes)
		}
	}
	var refusals []string
	refused, err := d.Read.Query("SELECT source || ' ' || details_json FROM operation_audit WHERE operation = 'settings.set' AND outcome = 'refused' AND target = ? ORDER BY id", pinned)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = refused.Close() }()
	for refused.Next() {
		var row string
		if err := refused.Scan(&row); err != nil {
			t.Fatal(err)
		}
		refusals = append(refusals, row)
	}
	if err := refused.Err(); err != nil {
		t.Fatal(err)
	}
	if want := []string{`cli {"keys":["` + pinned + `"]}`}; !slices.Equal(refusals, want) {
		t.Errorf("pinned refusal audit = %v, want %v", refusals, want)
	}
	rows, err := d.Read.Query("SELECT source, details_json FROM operation_audit WHERE operation = 'settings.set' AND outcome = 'invalid' AND target = ? ORDER BY id", key)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var got []string
	for rows.Next() {
		var source, details string
		if err := rows.Scan(&source, &details); err != nil {
			t.Fatal(err)
		}
		got = append(got, source+" "+details)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := []string{`cli {"keys":["` + key + `"]}`, `ui {"keys":["` + key + `"]}`}
	if !slices.Equal(got, want) {
		t.Errorf("refusal audit = %v, want %v", got, want)
	}
}

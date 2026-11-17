package main

import (
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/davmirror"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/unit"
)

func TestDeclaredSettingGroupSavesFromItsModulePage(t *testing.T) {
	t.Parallel()
	c := localWebG1(t, startSettingsServe(t, nil))
	apiData(t, c.do(http.MethodPatch, "/web/v1/settings", map[string]any{"set": map[string]any{"thread_cost_cap_usd": "12"}}), http.StatusOK)
	if row := runSettings(t, "get", "thread_cost_cap_usd").row(t); row.Value == nil || *row.Value != "12" || row.Source == nil || *row.Source != "db" {
		t.Errorf("thread_cost_cap_usd = %+v, want 12 from the database", row)
	}
	if row := runSettings(t, "get", "monthly_budget_usd").row(t); row.Source != nil && *row.Source == "db" {
		t.Errorf("saving one key stored another: %+v", row)
	}
	expectAPI(t, c.do(http.MethodPatch, "/web/v1/settings", map[string]any{"set": map[string]any{"thread_cost_cap_usd": "-1"}}), http.StatusUnprocessableEntity, "invalid")
	if row := runSettings(t, "get", "thread_cost_cap_usd").row(t); row.Value == nil || *row.Value != "12" {
		t.Errorf("a refused save changed thread_cost_cap_usd to %+v", row)
	}
	if got := c.do(http.MethodPatch, "/web/v1/settings", map[string]any{"set": map[string]any{"thread_cost_cap_usd": "13", "nope": "1"}}); got.status < http.StatusBadRequest {
		t.Errorf("PATCH an unknown key = %d: %s", got.status, got.body)
	}
	if row := runSettings(t, "get", "thread_cost_cap_usd").row(t); row.Value == nil || *row.Value != "12" {
		t.Errorf("a save naming an unknown key changed thread_cost_cap_usd to %+v", row)
	}
}

func TestModuleWithASectionGetsAPageAndKeepsRefusalsThere(t *testing.T) {
	t.Parallel()
	unsetEnv(t, settingEnv(davmirror.AppleIDKey))
	c := localWebG1(t, startSettingsServe(t, nil))
	apiData(t, c.do(http.MethodPatch, "/web/v1/settings", map[string]any{"set": map[string]any{"default_event_minutes": "45"}}), http.StatusOK)
	expectAPI(t, c.do(http.MethodPatch, "/web/v1/settings", map[string]any{"set": map[string]any{"default_event_minutes": "0"}}), http.StatusUnprocessableEntity, "invalid")
	if row := runSettings(t, "get", "default_event_minutes").row(t); row.Value == nil || *row.Value != "45" {
		t.Errorf("default_event_minutes = %+v, want 45", row)
	}
}

func TestMediaLibraryGroupEditsTheLinkTTL(t *testing.T) {
	t.Parallel()
	c := localWebG1(t, startSettingsServe(t, nil))
	apiData(t, c.do(http.MethodPatch, "/web/v1/settings", map[string]any{"set": map[string]any{"media_link_ttl_seconds": "7200"}}), http.StatusOK)
	if row := runSettings(t, "get", "media_link_ttl_seconds").row(t); row.Value == nil || *row.Value != "7200" {
		t.Errorf("media_link_ttl_seconds = %+v, want 7200", row)
	}
	expectAPI(t, c.do(http.MethodPatch, "/web/v1/settings", map[string]any{"set": map[string]any{"media_link_ttl_seconds": "30"}}), http.StatusUnprocessableEntity, "invalid")
	if row := runSettings(t, "get", "media_link_ttl_seconds").row(t); row.Value == nil || *row.Value != "7200" {
		t.Errorf("a refused TTL changed media_link_ttl_seconds to %+v", row)
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
	pinned := module.FlagKey(unit.Mail)
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
	c := localWebG1(t, base)
	expectAPI(t, c.do(http.MethodPatch, "/web/v1/settings", map[string]any{"set": map[string]any{key: "-1"}}), http.StatusUnprocessableEntity, "invalid")
	expectAPI(t, c.do(http.MethodPatch, "/web/v1/settings", map[string]any{"set": map[string]any{pinned: "false"}}), http.StatusConflict, "refused")
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
	want := []string{`cli {"keys":["` + key + `"]}`, `api {"keys":["` + key + `"]}`}
	if !slices.Equal(got, want) {
		t.Errorf("refusal audit = %v, want %v", got, want)
	}
}

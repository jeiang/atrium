package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/settings"
)

type settingRow struct {
	Key    string  `json:"key"`
	Value  *string `json:"value"`
	Source *string `json:"source"`
	Class  string  `json:"class"`
}

type settingsRun struct {
	code           int
	stdout, stderr string
}

func runSettings(t *testing.T, args ...string) settingsRun {
	t.Helper()
	var out, errs bytes.Buffer
	code := newApp().Run(context.Background(), append([]string{"settings"}, args...), &out, &errs)
	return settingsRun{code, out.String(), errs.String()}
}

func (r settingsRun) row(t *testing.T) settingRow {
	t.Helper()
	var row settingRow
	if err := json.Unmarshal([]byte(r.stdout), &row); err != nil {
		t.Fatalf("stdout %q is not a setting row: %v", r.stdout, err)
	}
	return row
}

func storedSetting(t *testing.T, path, key string) (string, bool) {
	t.Helper()
	d, err := db.Connect(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	var value string
	err = d.Read.QueryRow("SELECT value FROM settings WHERE key = ?", key).Scan(&value)
	return value, err == nil
}

func TestSettingsCommandsRoundTripThroughTheDatabaseAndTheBudgetReader(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "atrium.db")
	t.Setenv("ATRIUM_DB", path)
	t.Setenv("ATRIUM_CACHE_DIR", filepath.Join(dir, "cache"))
	t.Setenv("ATRIUM_PUBLIC_HOST", "")

	expect := func(got settingRow, value, source, class string) {
		t.Helper()
		if got.Value == nil || *got.Value != value || got.Source == nil || *got.Source != source || got.Class != class {
			t.Errorf("row = %+v, want value %s source %s class %s", got, value, source, class)
		}
	}

	list := runSettings(t, "list")
	var rows []settingRow
	if err := json.Unmarshal([]byte(list.stdout), &rows); err != nil || list.code != cli.ExitOK || list.stderr != "" {
		t.Fatalf("settings list: exit %d stderr %q: %v", list.code, list.stderr, err)
	}
	var listed []string
	for _, row := range rows {
		listed = append(listed, row.Key)
	}
	var registered []string
	for _, def := range settings.Default.Defs() {
		registered = append(registered, def.Key)
	}
	if !slices.Equal(listed, registered) || !slices.Contains(listed, "monthly_budget_usd") || !slices.Contains(listed, "cache_ttl_chat") {
		t.Errorf("list keys = %v, want every registered key %v", listed, registered)
	}

	expect(runSettings(t, "get", "monthly_budget_usd").row(t), "75", "default", "ui")

	set := runSettings(t, "set", "monthly_budget_usd", "200")
	if set.code != cli.ExitOK || set.stderr != "" {
		t.Fatalf("settings set: exit %d stderr %q", set.code, set.stderr)
	}
	expect(set.row(t), "200", "db", "ui")
	expect(runSettings(t, "get", "monthly_budget_usd").row(t), "200", "db", "ui")
	if v, ok := storedSetting(t, path, "monthly_budget_usd"); !ok || v != "200" {
		t.Errorf("stored monthly_budget_usd = %q, %v", v, ok)
	}

	t.Run("the budget reader sees the stored value", func(t *testing.T) {
		base := startServeIn(t, dir)
		status, body, _ := fetch(t, http.DefaultClient, "GET", base+"/settings/usage", nil)
		if status != http.StatusOK || !strings.Contains(body, "of $200 used") {
			t.Errorf("GET /settings/usage = %d, want the $200 budget: %.600s", status, body)
		}
	})

	for _, tc := range []struct{ key, value string }{
		{"monthly_budget_usd", "0x1p3"},
		{"monthly_budget_usd", "0"},
		{"cache_ttl_chat", "10m"},
	} {
		wantMessage := settings.Default.Validate(tc.key, tc.value)
		if wantMessage == nil {
			t.Fatalf("registry accepts %s=%s", tc.key, tc.value)
		}
		bad := runSettings(t, "set", tc.key, tc.value)
		if bad.code != cli.ExitError || bad.stdout != "" || bad.stderr != "error: "+wantMessage.Error()+"\n" {
			t.Errorf("set %s %s: exit %d stdout %q stderr %q, want exit 1 and %q", tc.key, tc.value, bad.code, bad.stdout, bad.stderr, wantMessage)
		}
	}
	if v, ok := storedSetting(t, path, "monthly_budget_usd"); !ok || v != "200" {
		t.Errorf("monthly_budget_usd after refused values = %q, %v; want 200", v, ok)
	}
	if _, ok := storedSetting(t, path, "cache_ttl_chat"); ok {
		t.Error("a refused cache_ttl_chat value was stored")
	}

	unset := runSettings(t, "unset", "monthly_budget_usd")
	if unset.code != cli.ExitOK {
		t.Fatalf("settings unset: exit %d stderr %q", unset.code, unset.stderr)
	}
	expect(unset.row(t), "75", "default", "ui")
	if _, ok := storedSetting(t, path, "monthly_budget_usd"); ok {
		t.Error("unset left the stored value")
	}

	for _, args := range [][]string{{"get", "no_such_key"}, {"set", "no_such_key", "1"}, {"unset", "no_such_key"}} {
		unknown := runSettings(t, args...)
		if unknown.code != cli.ExitUsage || unknown.stdout != "" || !strings.Contains(unknown.stderr, `error: settings: unknown key "no_such_key"`) {
			t.Errorf("settings %v: exit %d stdout %q stderr %q", args, unknown.code, unknown.stdout, unknown.stderr)
		}
	}
}

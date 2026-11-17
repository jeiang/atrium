package main

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/davmirror"
)

type settingsFieldCase struct {
	key, value string
}

func startSettingsServe(t *testing.T, values map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	setEnv(t, "ATRIUM_DB", filepath.Join(dir, "atrium.db"))
	setEnv(t, "ATRIUM_CACHE_DIR", filepath.Join(dir, "cache"))
	setEnv(t, "ATRIUM_PUBLIC_HOST", "")
	for key, value := range values {
		if ran := runSettings(t, "set", key, value); ran.code != 0 {
			t.Fatalf("settings set %s: %d %s", key, ran.code, ran.stderr)
		}
	}
	seedFlagsAt(t, newTestApp(t), filepath.Join(dir, "atrium.db"), allFlagsTrue())
	return startServeIn(t, dir)
}

func TestSettingsFieldsSaveFromTheirTabs(t *testing.T) {
	t.Parallel()
	unsetEnv(t, settingEnv(davmirror.AppleIDKey))
	web := webG4(t, startSettingsServe(t, nil))
	cases := []settingsFieldCase{
		{"calendar_account", "home"},
		{"icloud_apple_id", "operator@icloud.invalid"},
		{"icloud_calendar", "Home"},
		{"default_event_minutes", "30"},
		{"work_hours", "08:00-18:00"},
		{"work_days", "mon,tue,wed,thu,fri"},
		{"openloops_remind_hour", "8"},
		{"monthly_budget_usd", "200"},
	}
	for _, c := range cases {
		got := web.do(http.MethodPut, "/web/v1/settings/"+c.key, map[string]any{"value": c.value})
		if got.status != http.StatusOK {
			t.Errorf("PUT /web/v1/settings/%s %q = %d: %.300s", c.key, c.value, got.status, got.body)
			continue
		}
		row := runSettings(t, "get", c.key).row(t)
		if row.Value == nil || *row.Value != c.value || row.Source == nil || *row.Source != "db" {
			t.Errorf("%s = %+v, want %q from the database", c.key, row, c.value)
		}
	}
}

func TestSettingsFieldsRefuseInvalidValues(t *testing.T) {
	t.Parallel()
	unsetEnv(t, settingEnv(davmirror.AppleIDKey))
	web := webG4(t, startSettingsServe(t, nil))
	for _, c := range []struct {
		key, value, want string
	}{
		{"default_event_minutes", "0", "must be between 5 and 1440"},
		{"monthly_budget_usd", "0", "must be greater than 0"},
		{"work_hours", "9-5", "must be HH:MM-HH:MM"},
		{"work_days", "funday", "must be day names"},
	} {
		got := web.do(http.MethodPut, "/web/v1/settings/"+c.key, map[string]any{"value": c.value})
		if got.status != http.StatusUnprocessableEntity || !strings.Contains(got.body, c.want) {
			t.Errorf("PUT /web/v1/settings/%s %q = %d, want 422 with %q: %.300s", c.key, c.value, got.status, c.want, got.body)
		}
		if row := runSettings(t, "get", c.key).row(t); row.Value != nil && *row.Value == c.value {
			t.Errorf("%s stored the refused value %q", c.key, c.value)
		}
	}
}

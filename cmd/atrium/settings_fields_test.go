package main

import (
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/calendar"
)

type settingsFieldCase struct {
	page, action, key, value string
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
	unsetEnv(t, settingEnv(calendar.AppleIDKey))
	base := startSettingsServe(t, nil)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar}
	cases := []settingsFieldCase{
		{"/settings/notion", "/settings/groups/notion/wiki", "wiki_root", "0123456789abcdef0123456789abcdef"},
		{"/settings/notion", "/settings/groups/notion/wiki", "sensitivity_property", "sensitivity_level"},
		{"/settings/notion", "/settings/groups/notion/wiki", "notion_private_value", "confidential"},
		{"/settings/notion", "/settings/groups/notion/wiki", "wiki_log_title", "Wiki Log 2"},
		{"/settings/calendar", "/settings/sections/calendar", "calendar_account", "home"},
		{"/settings/calendar", "/settings/sections/calendar", "icloud_apple_id", "operator@icloud.invalid"},
		{"/settings/calendar", "/settings/sections/calendar", "icloud_calendar", "Home"},
		{"/settings/calendar", "/settings/sections/calendar", "default_event_minutes", "30"},
		{"/settings/calendar", "/settings/sections/calendar", "work_hours", "08:00-18:00"},
		{"/settings/calendar", "/settings/sections/calendar", "work_days", "mon,tue,wed,thu,fri"},
		{"/settings/mail", "/settings/mail/settings", "uncertain_dest", "Later"},
		{"/settings/mail", "/settings/mail/settings", "openloops_remind_hour", "8"},
		{"/settings/usage", "/settings/groups/core/budget", "monthly_budget_usd", "200"},
	}
	pages := map[string]string{}
	for _, c := range cases {
		body, ok := pages[c.page]
		if !ok {
			status, got, _ := fetch(t, client, http.MethodGet, base+c.page, nil)
			if status != http.StatusOK {
				t.Fatalf("GET %s = %d", c.page, status)
			}
			body = got
			pages[c.page] = got
		}
		if !strings.Contains(body, `name="`+c.key+`"`) {
			t.Errorf("%s does not render %s", c.page, c.key)
			continue
		}
		status, got, _ := fetch(t, client, http.MethodPost, base+c.action, url.Values{c.key: {c.value}})
		if status != http.StatusOK {
			t.Errorf("POST %s %s=%q = %d: %.300s", c.action, c.key, c.value, status, got)
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
	unsetEnv(t, settingEnv(calendar.AppleIDKey))
	base := startSettingsServe(t, nil)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar}
	if status, _, _ := fetch(t, client, http.MethodGet, base+"/settings/calendar", nil); status != http.StatusOK {
		t.Fatalf("GET /settings/calendar = %d", status)
	}
	for _, c := range []struct {
		action, key, value, want string
	}{
		{"/settings/sections/calendar", "default_event_minutes", "0", "must be between 5 and 1440"},
		{"/settings/groups/core/budget", "monthly_budget_usd", "0", "must be greater than 0"},
		{"/settings/sections/calendar", "work_hours", "9-5", "must be HH:MM-HH:MM"},
		{"/settings/sections/calendar", "work_days", "funday", "must be day names"},
		{"/settings/groups/notion/wiki", "wiki_root", "nope", "must be a Notion page id"},
	} {
		status, body, _ := fetch(t, client, http.MethodPost, base+c.action, url.Values{c.key: {c.value}})
		if status != http.StatusUnprocessableEntity || !strings.Contains(body, c.want) {
			t.Errorf("POST %s %s=%q = %d, want 422 with %q: %.300s", c.action, c.key, c.value, status, c.want, body)
		}
	}
}

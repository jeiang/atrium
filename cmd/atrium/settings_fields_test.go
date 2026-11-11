package main

import (
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/calendar"
	"github.com/jeiang/atrium/internal/notify"
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
	return startServeIn(t, dir)
}

func TestSettingsFieldsSaveFromTheirTabs(t *testing.T) {
	t.Parallel()
	unsetEnv(t, settingEnv(calendar.AppleIDKey), settingEnv(notify.KeyPushSubject))
	base := startSettingsServe(t, map[string]string{notify.KeyPushSubject: "mailto:ops@example.invalid"})
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar}
	cases := []settingsFieldCase{
		{"/settings/integrations", "/settings/integrations/notion/settings", "wiki_root", "0123456789abcdef0123456789abcdef"},
		{"/settings/integrations", "/settings/integrations/notion/settings", "sensitivity_property", "sensitivity"},
		{"/settings/integrations", "/settings/integrations/notion/settings", "notion_private_value", "private"},
		{"/settings/integrations", "/settings/integrations/notion/settings", "wiki_log_title", "Wiki Log"},
		{"/settings/integrations", "/settings/integrations/sections/calendar", "calendar_account", "home"},
		{"/settings/integrations", "/settings/integrations/sections/calendar", "icloud_apple_id", "operator@icloud.invalid"},
		{"/settings/integrations", "/settings/integrations/sections/calendar", "icloud_calendar", "Home"},
		{"/settings/integrations", "/settings/integrations/sections/calendar", "default_event_minutes", "30"},
		{"/settings/integrations", "/settings/integrations/sections/calendar", "work_hours", "08:00-18:00"},
		{"/settings/integrations", "/settings/integrations/sections/calendar", "work_days", "mon,tue,wed,thu,fri"},
		{"/settings/mail", "/settings/mail/settings", "uncertain_dest", "Later"},
		{"/settings/mail", "/settings/mail/settings", "openloops_remind_hour", "8"},
		{"/settings/notifications", "/settings/notifications/push/subject", "webpush_subject", "mailto:ops@example.invalid"},
		{"/settings/assistant", "/settings/groups/assistant/spending", "monthly_budget_usd", "200"},
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

func TestHostSettingsRenderReadOnlyAndIgnorePosts(t *testing.T) {
	t.Parallel()
	base := startSettingsServe(t, nil)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar}
	status, body, _ := fetch(t, client, http.MethodGet, base+"/settings/integrations", nil)
	if status != http.StatusOK {
		t.Fatalf("GET /settings/integrations = %d", status)
	}
	for _, key := range []string{"mail_chat_model", "grafana_metrics_uid", "grafana_logs_uid", "grafana_logs_type", "media_movies_url", "media_tv_url"} {
		if strings.Contains(body, `name="`+key+`"`) {
			t.Errorf("host setting %s renders an editable control", key)
		}
	}
	if !strings.Contains(body, "data-pinned") {
		t.Error("the integrations page does not mark host settings read-only")
	}
	for _, c := range []struct{ action, key, value string }{
		{"/settings/integrations/mail_chat/settings", "mail_chat_model", "qwen2"},
		{"/settings/integrations/grafana/settings", "grafana_metrics_uid", "metrics-uid"},
		{"/settings/integrations/sections/media", "media_movies_url", "https://media.invalid/movies"},
	} {
		if status, body, _ := fetch(t, client, http.MethodPost, base+c.action, url.Values{c.key: {c.value}}); status != http.StatusUnprocessableEntity || !strings.Contains(body, "host configuration and is not editable here") {
			t.Errorf("POST a host setting %s = %d, want a 422 refusal: %.200s", c.key, status, body)
		}
		row := runSettings(t, "get", c.key).row(t)
		if row.Source != nil && *row.Source == "db" {
			t.Errorf("a posted host setting %s was stored: %+v", c.key, row)
		}
	}
}

func TestSettingsFieldsRefuseInvalidValues(t *testing.T) {
	t.Parallel()
	unsetEnv(t, settingEnv(calendar.AppleIDKey), settingEnv(notify.KeyPushSubject))
	base := startSettingsServe(t, map[string]string{notify.KeyPushSubject: "mailto:ops@example.invalid"})
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar}
	if status, _, _ := fetch(t, client, http.MethodGet, base+"/settings/integrations", nil); status != http.StatusOK {
		t.Fatalf("GET /settings/integrations = %d", status)
	}
	for _, c := range []struct {
		action, key, value, want string
	}{
		{"/settings/integrations/sections/calendar", "default_event_minutes", "0", "must be between 5 and 1440"},
		{"/settings/groups/assistant/spending", "monthly_budget_usd", "0", "must be greater than 0"},
		{"/settings/integrations/sections/calendar", "work_hours", "9-5", "must be HH:MM-HH:MM"},
		{"/settings/integrations/sections/calendar", "work_days", "funday", "must be day names"},
		{"/settings/integrations/notion/settings", "wiki_root", "nope", "must be a Notion page id"},
		{"/settings/notifications/push/subject", "webpush_subject", "nope", "must be a mailto: or https: URL"},
	} {
		status, body, _ := fetch(t, client, http.MethodPost, base+c.action, url.Values{c.key: {c.value}})
		if status != http.StatusUnprocessableEntity || !strings.Contains(body, c.want) {
			t.Errorf("POST %s %s=%q = %d, want 422 with %q: %.300s", c.action, c.key, c.value, status, c.want, body)
		}
	}
}

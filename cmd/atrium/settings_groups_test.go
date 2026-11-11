package main

import (
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/calendar"
)

func TestDeclaredSettingGroupSavesFromItsModulePage(t *testing.T) {
	t.Parallel()
	base := startSettingsServe(t, nil)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar}
	status, body, _ := fetch(t, client, http.MethodGet, base+"/settings/assistant", nil)
	if status != http.StatusOK {
		t.Fatalf("GET /settings/assistant = %d", status)
	}
	for _, want := range []string{`data-group="assistant.spending"`, `name="thread_cost_cap_usd"`, "Thread cost cap (USD)"} {
		if !strings.Contains(body, want) {
			t.Errorf("/settings/assistant does not render %s", want)
		}
	}
	const action = "/settings/groups/assistant/spending"
	status, body, _ = fetch(t, client, http.MethodPost, base+action, url.Values{"thread_cost_cap_usd": {"12"}, "monthly_budget_usd": {"150"}})
	if status != http.StatusOK || !strings.Contains(body, "data-saved") {
		t.Fatalf("POST %s = %d, saved marker %v", action, status, strings.Contains(body, "data-saved"))
	}
	if row := runSettings(t, "get", "thread_cost_cap_usd").row(t); row.Value == nil || *row.Value != "12" || row.Source == nil || *row.Source != "db" {
		t.Errorf("thread_cost_cap_usd = %+v, want 12 from the database", row)
	}
	if row := runSettings(t, "get", "monthly_budget_usd").row(t); row.Source != nil && *row.Source == "db" {
		t.Errorf("saving the group stored an unchanged key: %+v", row)
	}
	status, body, _ = fetch(t, client, http.MethodPost, base+action, url.Values{"thread_cost_cap_usd": {"-1"}})
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
	status, body, _ := fetch(t, client, http.MethodGet, base+"/settings/calendar", nil)
	if status != http.StatusOK {
		t.Fatalf("GET /settings/calendar = %d", status)
	}
	for _, want := range []string{`data-section="calendar"`, `name="default_event_minutes"`, `name="back" value="/settings/calendar"`, `data-module="calendar"`} {
		if !strings.Contains(body, want) {
			t.Errorf("/settings/calendar does not render %s", want)
		}
	}
	const action = "/settings/sections/calendar"
	status, body, _ = fetch(t, client, http.MethodPost, base+action, url.Values{"default_event_minutes": {"45"}, "back": {"/settings/calendar"}})
	if status != http.StatusOK || !strings.Contains(body, "<title>Calendar · Settings") || !strings.Contains(body, "data-saved") || !strings.Contains(body, `value="45"`) {
		t.Errorf("saving from the module page = %d, back on the page %v, saved %v", status, strings.Contains(body, "<title>Calendar · Settings"), strings.Contains(body, "data-saved"))
	}
	status, body, _ = fetch(t, client, http.MethodPost, base+action, url.Values{"default_event_minutes": {"0"}, "back": {"/settings/calendar"}})
	if status != http.StatusUnprocessableEntity || !strings.Contains(body, "<title>Calendar · Settings") || !strings.Contains(body, "must be between 5 and 1440") {
		t.Errorf("a refused value = %d, on the module page %v: %.200s", status, strings.Contains(body, "<title>Calendar · Settings"), body)
	}
	if row := runSettings(t, "get", "default_event_minutes").row(t); row.Value == nil || *row.Value != "45" {
		t.Errorf("default_event_minutes = %+v, want 45", row)
	}
}

package main

import (
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/contacts"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/settings"
)

type settingsSite struct {
	t      *testing.T
	base   string
	client *http.Client
}

func (s *settingsSite) get(path string) (int, string) {
	s.t.Helper()
	code, body, _ := fetch(s.t, s.client, http.MethodGet, s.base+path, nil)
	return code, body
}

func (s *settingsSite) post(path string, form url.Values) (int, string) {
	s.t.Helper()
	code, body, _ := fetch(s.t, s.client, http.MethodPost, s.base+path, form)
	return code, body
}

func TestSettingsPagesCoverTheRegistryAndSaveThroughTheAudit(t *testing.T) {
	dir, _ := moduleFixture(t, flagsExcept(module.Contacts, ""))
	unsetEnv(t, flagEnv(module.Contacts), settingEnv(contacts.DirKey))
	jar, _ := cookiejar.New(nil)
	s := &settingsSite{t: t, base: startServeIn(t, dir), client: &http.Client{Jar: jar}}
	db := filepath.Join(dir, "atrium.db")

	pages := map[string]string{}
	seen := map[string]int{}
	for _, d := range settings.Default.Defs() {
		if _, ok := pages[d.Module]; ok {
			continue
		}
		code, body := s.get("/settings/config/" + d.Module)
		pages[d.Module] = body
		if code != http.StatusOK && !d.Generated {
			t.Fatalf("%s page: %d", d.Module, code)
		}
	}
	for _, body := range pages {
		for _, d := range settings.Default.Defs() {
			seen[d.Key] += strings.Count(body, `data-setting="`+d.Key+`"`)
		}
	}
	for _, d := range settings.Default.Defs() {
		want := 1
		if d.Generated {
			want = 0
		}
		if seen[d.Key] != want {
			t.Errorf("%s appears %d times on the generated pages, want %d", d.Key, seen[d.Key], want)
		}
	}
	if !strings.Contains(pages["core"], `href="/settings/modules" class="text-xs`) {
		t.Errorf("module flags do not point at the modules page")
	}

	const key = "max_steps_chat"
	if code, body := s.post("/settings/config/assistant", url.Values{"v." + key: {"many"}}); code != http.StatusUnprocessableEntity || !strings.Contains(body, `data-setting="`+key+`" data-failed`) {
		t.Fatalf("invalid value: %d", code)
	}
	if v, ok := storedSetting(t, db, key); ok {
		t.Fatalf("invalid value stored: %q", v)
	}
	if code, _ := s.post("/settings/config/assistant", url.Values{"v." + key: {"30"}, "v.max_steps_cron": {"24"}}); code != http.StatusOK {
		t.Fatalf("save: %d", code)
	}
	if v, _ := storedSetting(t, db, key); v != "30" {
		t.Fatalf("stored %s = %q", key, v)
	}
	if v, ok := storedSetting(t, db, "max_steps_cron"); ok {
		t.Errorf("an unchanged field was stored: %q", v)
	}
	if code, _ := s.post("/settings/config/assistant", url.Values{"reset": {key}}); code != http.StatusOK {
		t.Fatalf("reset: %d", code)
	}
	if v, ok := storedSetting(t, db, key); ok {
		t.Fatalf("reset left %q", v)
	}
	_, audit := s.get("/settings/audit")
	if strings.Count(audit, `data-audit="`+key+`"`) != 2 || !strings.Contains(audit, "operator · ui") {
		t.Errorf("audit view does not show the save and the reset")
	}

	flag := module.FlagKey(module.Contacts)
	toggle := "/settings/modules/" + module.Contacts
	if code, body := s.post(toggle, url.Values{"on": {"true"}}); code != http.StatusUnprocessableEntity || !strings.Contains(body, contacts.DirKey) {
		t.Fatalf("enable without its directory: %d", code)
	}
	if v, _ := storedSetting(t, db, flag); v == "true" {
		t.Fatal("contacts enabled without its directory")
	}
	dirDef, _ := settings.Default.Lookup(contacts.DirKey)
	if code, _ := s.post("/settings/config/"+dirDef.Module, url.Values{"v." + contacts.DirKey: {filepath.Join(dir, "contacts")}}); code != http.StatusOK {
		t.Fatalf("save directory: %d", code)
	}
	code, body := s.post(toggle, url.Values{"on": {"true"}})
	if code != http.StatusOK {
		t.Fatalf("enable: %d", code)
	}
	if v, _ := storedSetting(t, db, flag); v != "true" {
		t.Fatalf("stored %s = %q", flag, v)
	}
	if !strings.Contains(body, `data-module="contacts" data-state="enabled"`) || !strings.Contains(body, "data-restart") {
		t.Errorf("modules page after enabling contacts does not show it enabled pending a restart")
	}
	if !strings.Contains(body, `data-module="mail" data-state="enabled"`) || strings.Contains(body, `action="/settings/modules/core"`) {
		t.Errorf("modules page lists the wrong modules")
	}
}

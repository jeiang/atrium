package main

import (
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/notify"
)

func ntfyServer(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc((&healthServer{status: http.StatusOK}).handler))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestConnectingAnIntegrationTurnsItsModuleOn(t *testing.T) {
	t.Parallel()
	ntfy := ntfyServer(t)
	dir, _, _ := moduleFixture(t, flagsExcept(module.Notifications, ""))
	setEnv(t, settingEnv(notify.KeyURL), "")
	db := filepath.Join(dir, "atrium.db")
	flag := module.FlagKey(module.Notifications)
	s := &integrationSite{t: t, dir: dir}
	s.start()

	if code, _, _ := fetch(t, s.client, http.MethodGet, s.base+"/settings/notifications", nil); code != http.StatusNotFound {
		t.Fatalf("notifications page before connecting: %d, want 404", code)
	}
	if strings.Contains(s.page(), `href="/settings/notifications"`) {
		t.Fatal("the Notifications tab shows while the module is off")
	}
	code, body := s.post("/ntfy/test", url.Values{"url": {ntfy}})
	if code != http.StatusOK || !strings.Contains(body, `data-module-hold="enabled"`) {
		t.Fatalf("connect ntfy: %d, want the notifications module reported on", code)
	}
	if v, _ := storedSetting(t, db, flag); v != "true" {
		t.Fatalf("stored %s = %q", flag, v)
	}
	var actor, source string
	if err := mustConnect(t, db).Read.QueryRow("SELECT actor, source FROM settings_audit WHERE key = ? ORDER BY id DESC LIMIT 1", flag).Scan(&actor, &source); err != nil || actor != "operator" || source != "ui" {
		t.Fatalf("flag audit = %q/%q (%v), want operator/ui", actor, source, err)
	}

	s.stop()
	s.start()
	if code, _, _ := fetch(t, s.client, http.MethodGet, s.base+"/settings/notifications", nil); code != http.StatusOK {
		t.Fatalf("notifications page after connecting and restarting: %d", code)
	}
	if !strings.Contains(s.page(), `href="/settings/notifications"`) {
		t.Error("the Notifications tab is missing after the module turned on")
	}
}

func TestPinnedOffModuleStaysOffWhenItsIntegrationConnects(t *testing.T) {
	t.Parallel()
	ntfy := ntfyServer(t)
	dir, _, _ := moduleFixture(t, flagsExcept(module.Notifications, "false"))
	setEnv(t, settingEnv(notify.KeyURL), "")
	db := filepath.Join(dir, "atrium.db")
	s := &integrationSite{t: t, dir: dir}
	s.start()

	code, body := s.post("/ntfy/test", url.Values{"url": {ntfy}})
	if code != http.StatusOK {
		t.Fatalf("connect ntfy with notifications pinned off: %d", code)
	}
	if !strings.Contains(body, `data-module-hold="pinned"`) || !strings.Contains(body, "pinned by env "+flagEnv(module.Notifications)) {
		t.Fatal("the integrations page does not report the pinned-off module")
	}
	if got := storedURL(t, dir); got != ntfy {
		t.Fatalf("stored ntfy URL %q, want the connected one", got)
	}
	if v, ok := storedSetting(t, db, module.FlagKey(module.Notifications)); ok {
		t.Fatalf("a pinned-off flag was stored: %q", v)
	}
	s.stop()
	s.start()
	if code, _, _ := fetch(t, s.client, http.MethodGet, s.base+"/settings/notifications", nil); code != http.StatusNotFound {
		t.Errorf("notifications page after restart: %d, want 404 while pinned off", code)
	}
}

func TestModulesSettingsPageIsGone(t *testing.T) {
	t.Parallel()
	base := startServe(t)
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	code, body, _ := fetch(t, client, http.MethodGet, base+"/settings/integrations", nil)
	if code != http.StatusOK || strings.Contains(body, `href="/settings/modules"`) {
		t.Fatalf("integrations page: %d, or it still links the modules page", code)
	}
	if code, _, _ := fetch(t, client, http.MethodGet, base+"/settings/modules", nil); code != http.StatusNotFound {
		t.Errorf("GET /settings/modules = %d, want 404", code)
	}
	if code, _, _ := fetch(t, client, http.MethodPost, base+"/settings/modules/"+module.Contacts, url.Values{"on": {"true"}}); code != http.StatusNotFound {
		t.Errorf("POST /settings/modules/%s = %d, want 404", module.Contacts, code)
	}
}

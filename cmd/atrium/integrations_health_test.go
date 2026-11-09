//go:build !modernc

package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	grafanatools "github.com/jeiang/atrium/internal/assistant/tools/grafana"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/integrations"
	"github.com/jeiang/atrium/internal/media"
	"github.com/jeiang/atrium/internal/module"
)

func TestIntegrationTestSavesOnlyWorkingConfigAndHealthNotifiesOncePerChange(t *testing.T) {
	good := &healthServer{status: http.StatusOK}
	goodSrv := httptest.NewServer(http.HandlerFunc(good.handler))
	t.Cleanup(goodSrv.Close)
	bad := &healthServer{status: http.StatusInternalServerError}
	badSrv := httptest.NewServer(http.HandlerFunc(bad.handler))
	t.Cleanup(badSrv.Close)

	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	setEnv(t, "ATRIUM_DB_KEY", rightKeyHex)
	t.Cleanup(func() { db.SetDefaultKey(db.Key{}) })
	setEnv(t, settingEnv(grafanatools.KeyURL), "")
	unsetEnv(t, settingEnv(grafanatools.KeyToken))
	setEnv(t, settingEnv(media.KeyQbitURL), "")
	push, browser := pushRig(t, dir)
	if got := runApp(t, "settings", "list"); got.code != 0 {
		t.Fatalf("settings list: %d %s", got.code, got.stderr)
	}
	browser.store(t, mustConnect(t, filepath.Join(dir, "atrium.db")))
	notices := func() []string { return highPriority(t, push, browser) }
	s := &integrationSite{t: t, dir: dir}

	t.Run("a failed test keeps the stored config", func(t *testing.T) {
		s.t = t
		s.start()
		page := s.page()
		if !strings.Contains(page, `data-integration="grafana" data-state="untested"`) || !strings.Contains(page, `id="url-grafana" name="url"`) {
			t.Fatal("grafana is not listed as an untested editable integration")
		}
		if !strings.Contains(page, `id="url-qbittorrent" class="break-all font-mono text-xs" data-pinned`) {
			t.Error("the deployment-only qBittorrent URL is editable")
		}
		if code, body := s.post("/grafana/test", url.Values{"url": {badSrv.URL}, "secret": {"viewer-token"}}); code != http.StatusUnprocessableEntity || !strings.Contains(body, "test failed (failing)") {
			t.Fatalf("failing test: %d", code)
		}
		if got := storedURL(t, dir); got != "" {
			t.Fatalf("a failed test stored %q", got)
		}
		if code, _ := s.post("/grafana/test", url.Values{"url": {goodSrv.URL}, "secret": {"viewer-token"}}); code != http.StatusOK {
			t.Fatalf("passing test: %d", code)
		}
		if got := storedURL(t, dir); got != goodSrv.URL || health(t, dir, "grafana") != integrations.OK {
			t.Fatalf("after a passing test: url %q state %s", got, health(t, dir, "grafana"))
		}
		if code, body := s.post("/grafana/test", url.Values{"url": {badSrv.URL}, "secret": {"viewer-token"}}); code != http.StatusUnprocessableEntity || !strings.Contains(body, "test failed") {
			t.Fatalf("failing test over a working one: %d", code)
		}
		if got := storedURL(t, dir); got != goodSrv.URL || health(t, dir, "grafana") != integrations.OK {
			t.Errorf("a failed test replaced the working record: url %q state %s", got, health(t, dir, "grafana"))
		}
	})

	t.Run("the daily check moves through failing and needs re-auth", func(t *testing.T) {
		s.t = t
		good.set(http.StatusInternalServerError)
		s.start()
		waitFor(t, func() bool { return health(t, dir, "grafana") == integrations.Failing })
		if !strings.Contains(s.page(), "data-integration-alerts") {
			t.Error("no admin banner for a failing integration")
		}
		waitFor(t, func() bool { return len(notices()) == 1 })
	})

	t.Run("an unchanged state sends nothing", func(t *testing.T) {
		s.t = t
		s.start()
		time.Sleep(300 * time.Millisecond)
		if n := len(notices()); n != 1 {
			t.Errorf("%d notices after a restart in the same state, want 1", n)
		}
	})

	t.Run("refused credentials need re-auth", func(t *testing.T) {
		s.t = t
		good.set(http.StatusUnauthorized)
		s.start()
		waitFor(t, func() bool { return health(t, dir, "grafana") == integrations.NeedsReauth })
		waitFor(t, func() bool { return len(notices()) == 2 })
	})

	t.Run("disconnect unsets the record and keeps the data", func(t *testing.T) {
		s.t = t
		good.set(http.StatusOK)
		s.start()
		if code, _ := s.post("/grafana/disconnect", nil); code != http.StatusOK {
			t.Fatalf("disconnect: %d", code)
		}
		if got := storedURL(t, dir); got != "" || health(t, dir, "grafana") != integrations.Untested {
			t.Fatalf("after disconnect: url %q state %s", got, health(t, dir, "grafana"))
		}
		s.stop()
		d := mustConnect(t, filepath.Join(dir, "atrium.db"))
		var kept int
		if err := d.Read.QueryRow("SELECT COUNT(*) FROM notifications").Scan(&kept); err != nil || kept == 0 {
			t.Errorf("notifications after disconnect: %d (%v), want the rows kept", kept, err)
		}
	})
}

func grafanaSite(t *testing.T, flag string) (*integrationSite, string, string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc((&healthServer{status: http.StatusOK}).handler))
	t.Cleanup(srv.Close)
	dir, _, _ := moduleFixture(t, flagsExcept(module.Grafana, flag))
	setEnv(t, "ATRIUM_DB_KEY", rightKeyHex)
	t.Cleanup(func() { db.SetDefaultKey(db.Key{}) })
	setEnv(t, settingEnv(grafanatools.KeyURL), "")
	unsetEnv(t, settingEnv(grafanatools.KeyToken))
	s := &integrationSite{t: t, dir: dir}
	s.start()
	return s, srv.URL, filepath.Join(dir, "atrium.db")
}

func TestConnectingAnIntegrationTurnsItsModuleOn(t *testing.T) {
	s, grafana, path := grafanaSite(t, "")
	flag := module.FlagKey(module.Grafana)
	code, body := s.post("/grafana/test", url.Values{"url": {grafana}, "secret": {"viewer-token"}})
	if code != http.StatusOK || !strings.Contains(body, `data-module-hold="enabled"`) {
		t.Fatalf("connect grafana: %d, want the grafana module reported on", code)
	}
	if v, _ := storedSetting(t, path, flag); v != "true" {
		t.Fatalf("stored %s = %q", flag, v)
	}
	var actor, source string
	if err := mustConnect(t, path).Read.QueryRow("SELECT actor, source FROM settings_audit WHERE key = ? ORDER BY id DESC LIMIT 1", flag).Scan(&actor, &source); err != nil || actor != "operator" || source != "ui" {
		t.Fatalf("flag audit = %q/%q (%v), want operator/ui", actor, source, err)
	}
}

func TestPinnedOffModuleStaysOffWhenItsIntegrationConnects(t *testing.T) {
	s, grafana, path := grafanaSite(t, "false")
	code, body := s.post("/grafana/test", url.Values{"url": {grafana}, "secret": {"viewer-token"}})
	if code != http.StatusOK {
		t.Fatalf("connect grafana with grafana pinned off: %d", code)
	}
	if !strings.Contains(body, `data-module-hold="pinned"`) || !strings.Contains(body, "pinned by env "+flagEnv(module.Grafana)) {
		t.Fatal("the integrations page does not report the pinned-off module")
	}
	if got := storedURL(t, s.dir); got != grafana {
		t.Fatalf("stored grafana URL %q, want the connected one", got)
	}
	if v, ok := storedSetting(t, path, module.FlagKey(module.Grafana)); ok {
		t.Fatalf("a pinned-off flag was stored: %q", v)
	}
}

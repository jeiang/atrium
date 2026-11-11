//go:build !modernc

package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"

	grafanatools "github.com/jeiang/atrium/internal/assistant/tools/grafana"
)

func grafanaHealthServer(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var status atomic.Int32
	status.Store(http.StatusOK)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch code := status.Load(); {
		case code != http.StatusOK:
			w.WriteHeader(int(code))
		case r.URL.Path == "/api/user":
			_, _ = w.Write([]byte(`{"login":"sa-atrium"}`))
		case r.URL.Path == "/api/search":
			_, _ = w.Write([]byte(`[]`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &status
}

func connectionNotices(t *testing.T, dir, label string) int {
	t.Helper()
	var n int
	if err := mustConnect(t, filepath.Join(dir, "atrium.db")).Read.QueryRow(
		"SELECT COUNT(*) FROM notifications WHERE title LIKE ?", "atrium: "+label+" %").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestConnectionHealthNotifiesOncePerTransitionAndDisconnectKeepsData(t *testing.T) {
	requireCipher(t)
	srv, status := grafanaHealthServer(t)
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	setEnv(t, "ATRIUM_DB_KEY", rightKeyHex)
	t.Cleanup(func() { db.SetDefaultKey(db.Key{}) })
	setEnv(t, "ATRIUM_HOST_CONFIG", servicesHostConfig(t, dir, srv.URL, "http://anthropic.invalid"))
	setEnv(t, settingEnv(grafanatools.KeyURL), srv.URL)
	s := &integrationSite{t: t, dir: dir}
	s.start()

	if code, body := s.conn("/test", url.Values{"id": {"service/grafana"}, "api_key": {"glsa_good"}}); code != http.StatusOK {
		t.Fatalf("grafana test: %d %s", code, body)
	}
	if state := connectionState(t, dir, "service/grafana"); state != "ok" {
		t.Fatalf("state after a passing test = %q", state)
	}
	if n := connectionNotices(t, dir, "Grafana"); n != 0 {
		t.Fatalf("%d notices for the first observation, want 0", n)
	}

	status.Store(http.StatusInternalServerError)
	if got := runApp(t, "connections", "test", "service/grafana"); got.code != cli.ExitError || !strings.Contains(got.stdout+got.stderr, "failing") {
		t.Fatalf("failing test: exit %d stdout %q stderr %q", got.code, got.stdout, got.stderr)
	}
	if state := connectionState(t, dir, "service/grafana"); state != "failing" {
		t.Fatalf("state after a failing test = %q", state)
	}
	if n := connectionNotices(t, dir, "Grafana"); n != 1 {
		t.Fatalf("%d notices after one transition, want 1", n)
	}
	if got := runApp(t, "connections", "test", "service/grafana"); got.code != cli.ExitError {
		t.Fatalf("second failing test: exit %d", got.code)
	}
	if n := connectionNotices(t, dir, "Grafana"); n != 1 {
		t.Fatalf("%d notices after an unchanged failing check, want 1", n)
	}

	status.Store(http.StatusUnauthorized)
	if got := runApp(t, "connections", "test", "service/grafana"); got.code != cli.ExitError {
		t.Fatalf("refused token: exit %d", got.code)
	}
	if state := connectionState(t, dir, "service/grafana"); state != "needs_reauth" {
		t.Fatalf("state after a refused token = %q", state)
	}
	if n := connectionNotices(t, dir, "Grafana"); n != 2 {
		t.Fatalf("%d notices after a second transition, want 2", n)
	}

	s.stop()
	s.start()
	waitFor(t, func() bool { return connectionState(t, dir, "service/grafana") == "needs_reauth" })
	if n := connectionNotices(t, dir, "Grafana"); n != 2 {
		t.Fatalf("%d notices after a restart in the same state, want 2", n)
	}

	before := connectionNotices(t, dir, "Grafana")
	if code, body := s.conn("/disconnect", url.Values{"id": {"service/grafana"}}); code != http.StatusSeeOther && code != http.StatusOK {
		t.Fatalf("disconnect: %d %s", code, body)
	}
	if state := connectionState(t, dir, "service/grafana"); state != "unconfigured" {
		t.Fatalf("state after disconnect = %q", state)
	}
	if after := connectionNotices(t, dir, "Grafana"); after != before || after == 0 {
		t.Fatalf("notifications after disconnect: %d, want the %d rows kept", after, before)
	}
}

//go:build !modernc

package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db/dbtest"
	"github.com/jeiang/atrium/internal/endpoint"
	"github.com/jeiang/atrium/internal/module"
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
	if err := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t)).Read.QueryRow(
		"SELECT COUNT(*) FROM notifications WHERE title LIKE ?", "atrium: "+label+" %").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestConnectionHealthNotifiesOncePerTransitionAndDisconnectKeepsData(t *testing.T) {
	t.Parallel()
	requireCipher(t)
	srv, status := grafanaHealthServer(t)
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	setEnv(t, "ATRIUM_DB_KEY", rightKeyHex)
	setEnv(t, "ATRIUM_HOST_CONFIG", servicesHostConfig(t, dir, srv.URL, "http://anthropic.invalid"))
	s := &connectionSite{t: t, dir: dir}
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

func TestOpsMetricsReportAConnectionStateBeforeAndAfterAFailingTest(t *testing.T) {
	t.Parallel()
	requireCipher(t)
	srv, status := grafanaHealthServer(t)
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	setEnv(t, "ATRIUM_DB_KEY", rightKeyHex)
	setEnv(t, "ATRIUM_HOST_CONFIG", servicesHostConfig(t, dir, srv.URL, "http://anthropic.invalid"))
	ops := opsAddr(t)
	s := &connectionSite{t: t, dir: dir}
	s.start()
	if code, body := s.conn("/test", url.Values{"id": {"service/grafana"}, "api_key": {"glsa_good"}}); code != http.StatusOK {
		t.Fatalf("grafana test: %d %s", code, body)
	}
	scrape := func() string {
		code, body, _ := fetch(t, http.DefaultClient, http.MethodGet, ops+"/metrics", nil)
		if code != http.StatusOK {
			t.Fatalf("GET /metrics = %d", code)
		}
		return body
	}
	if body := scrape(); !strings.Contains(body, `atrium_integration_state{integration="service/grafana",state="ok"} 1`) {
		t.Fatalf("metrics before a failing test:\n%s", body)
	}
	s.stop()
	status.Store(http.StatusInternalServerError)
	if got := runApp(t, "connections", "test", "service/grafana"); got.code != cli.ExitError {
		t.Fatalf("failing test: exit %d stdout %q stderr %q", got.code, got.stdout, got.stderr)
	}
	s.start()
	defer s.stop()
	if body := scrape(); !strings.Contains(body, `atrium_integration_state{integration="service/grafana",state="failing"} 1`) {
		t.Fatalf("metrics after a failing test:\n%s", body)
	}
}

func TestAutomaticEmbeddingFailureDoesNotHoldMail(t *testing.T) {
	chat, embed := newFakeOpenAI(t), newFakeOpenAI(t)
	dir, api, _ := moduleFixture(t, flagsExcept(module.Mail, ""))
	setEnv(t, settingEnv(endpoint.KeyChatURL), chat.URL+"/v1")
	setEnv(t, "ATRIUM_HOST_CONFIG", embeddingHostWith(t, dir, embed.URL+"/v1", "embeddinggemma", "", ""))

	if got := runApp(t, "connections", "test", "service/embedding"); got.code != cli.ExitOK || !strings.Contains(got.stdout, "ok") {
		t.Fatalf("passing Test: exit %d stdout %q stderr %q", got.code, got.stdout, got.stderr)
	}
	if on := runSettings(t, "set", module.FlagKey(module.Mail), "true"); on.code != cli.ExitOK {
		t.Fatalf("enabling mail after a passing Test: %d %q", on.code, on.stderr)
	}
	if names := offeredTools(t, api, runApp(t, "agent", "run", "--text", "hello", "--kind", "chat")); !slices.Contains(names, "mail_search") {
		t.Fatalf("mail tools before the outage: %v", names)
	}

	d := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
	if _, err := d.Write.Exec("INSERT INTO accounts (name, kind) VALUES ('icloud', 'icloud')"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.Exec("INSERT INTO mailboxes (id, account, name, imap_name, role) VALUES (1, 'icloud', 'INBOX', 'INBOX', 'inbox')"); err != nil {
		t.Fatal(err)
	}
	dbtest.AddMessage(t, d, 1, "a message to embed", map[string]any{"subject": "Outage", "from_addr": "seed@x.test"})
	if _, err := d.Write.Exec("INSERT OR REPLACE INTO embed_index (generation, state, url, model, dimension) VALUES (0, 'active', '', 'embeddinggemma', ?)", config.EmbedDim); err != nil {
		t.Fatal(err)
	}
	embed.mu.Lock()
	embed.dim = config.EmbedDim
	embed.mu.Unlock()
	var checked float64
	if err := d.Read.QueryRow("SELECT checked_at FROM connections WHERE id = 'service/embedding'").Scan(&checked); err != nil {
		t.Fatal(err)
	}
	s := &connectionSite{t: t, dir: dir}
	s.start()
	waitFor(t, func() bool {
		var next float64
		return d.Read.QueryRow("SELECT checked_at FROM connections WHERE id = 'service/embedding'").Scan(&next) == nil && next > checked
	})

	embed.setStatus(http.StatusInternalServerError)
	if _, err := d.Write.Exec("UPDATE connections SET checked_at = checked_at - 600 WHERE id = 'service/embedding'"); err != nil {
		t.Fatal(err)
	}
	runApp(t, "reindex")
	waitFor(t, func() bool { return connectionState(t, dir, "service/embedding") == "failing" })
	if n := connectionNotices(t, dir, "Embedding"); n != 1 {
		t.Fatalf("%d notices after the automatic failure, want 1", n)
	}
	if on := runSettings(t, "set", module.FlagKey(module.Mail), "true"); on.code != cli.ExitOK {
		t.Fatalf("mail held by an automatic failure: %d %q", on.code, on.stderr)
	}
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("hi")))
	if names := offeredTools(t, api, runApp(t, "agent", "run", "--text", "hello", "--kind", "chat")); !slices.Contains(names, "mail_search") {
		t.Fatalf("mail tools during the outage: %v", names)
	}
	if listed := runApp(t, "connections", "list"); listed.code != cli.ExitOK || !strings.Contains(listed.stdout, "service/embedding") || !strings.Contains(listed.stdout, "failing") {
		t.Fatalf("connections list during the outage: %d %q", listed.code, listed.stdout)
	}

	embed.setStatus(0)
	s.stop()
	s.start()
	waitFor(t, func() bool { return connectionState(t, dir, "service/embedding") == "ok" })
}

func TestAnExplicitEmbeddingFailureHoldsMailUntilAnExplicitTestPasses(t *testing.T) {
	t.Parallel()
	requireCipher(t)
	chat, embed := newFakeOpenAI(t), newFakeOpenAI(t)
	dir, _, _ := moduleFixture(t, flagsExcept(module.Mail, ""))
	setEnv(t, settingEnv(endpoint.KeyChatURL), chat.URL+"/v1")
	setEnv(t, "ATRIUM_HOST_CONFIG", embeddingHostWith(t, dir, embed.URL+"/v1", "embeddinggemma", "", ""))

	embed.setStatus(http.StatusInternalServerError)
	if got := runApp(t, "connections", "test", "service/embedding"); got.code != cli.ExitError {
		t.Fatalf("failing Test: exit %d stdout %q stderr %q", got.code, got.stdout, got.stderr)
	}
	if on := runSettings(t, "set", module.FlagKey(module.Mail), "true"); on.code == cli.ExitOK {
		t.Fatalf("mail enabled after an explicit failure: %q", on.stdout)
	} else if !strings.Contains(on.stderr+on.stdout, "embed endpoint probe failed") {
		t.Fatalf("the hold message: %q", on.stderr+on.stdout)
	}

	embed.setStatus(0)
	s := &connectionSite{t: t, dir: dir}
	s.start()
	waitFor(t, func() bool { return connectionState(t, dir, "service/embedding") == "ok" })
	if on := runSettings(t, "set", module.FlagKey(module.Mail), "true"); on.code == cli.ExitOK {
		t.Fatalf("an automatic pass released the hold: %q", on.stdout)
	}
	s.stop()

	if got := runApp(t, "connections", "test", "service/embedding"); got.code != cli.ExitOK {
		t.Fatalf("passing Test: exit %d stdout %q stderr %q", got.code, got.stdout, got.stderr)
	}
	if on := runSettings(t, "set", module.FlagKey(module.Mail), "true"); on.code != cli.ExitOK {
		t.Fatalf("mail not released after a passing Test: %d %q", on.code, on.stderr)
	}

	embed.setStatus(http.StatusInternalServerError)
	s.start()
	defer s.stop()
	waitFor(t, func() bool { return connectionState(t, dir, "service/embedding") == "failing" })
	if on := runSettings(t, "set", module.FlagKey(module.Mail), "true"); on.code != cli.ExitOK {
		t.Fatalf("an automatic failure held mail: %d %q", on.code, on.stderr)
	}
}

func TestConcurrentConnectionTestsRecordOneNotification(t *testing.T) {
	t.Parallel()
	requireCipher(t)
	srv, status := grafanaHealthServer(t)
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	setEnv(t, "ATRIUM_DB_KEY", rightKeyHex)
	setEnv(t, "ATRIUM_HOST_CONFIG", servicesHostConfig(t, dir, srv.URL, "http://anthropic.invalid"))
	s := &connectionSite{t: t, dir: dir}
	s.start()
	defer s.stop()
	if code, body := s.conn("/test", url.Values{"id": {"service/grafana"}, "api_key": {"glsa_good"}}); code != http.StatusOK && code != http.StatusSeeOther {
		t.Fatalf("grafana test: %d %s", code, body)
	}

	for i := 0; i < 5; i++ {
		status.Store(http.StatusOK)
		if got := runApp(t, "connections", "test", "service/grafana"); got.code != cli.ExitOK {
			t.Fatalf("passing test %d: exit %d stdout %q stderr %q", i, got.code, got.stdout, got.stderr)
		}
		before := connectionNotices(t, dir, "Grafana")
		status.Store(http.StatusInternalServerError)
		var wg sync.WaitGroup
		codes := make([]int, 2)
		for j := range codes {
			wg.Add(1)
			go func() {
				defer wg.Done()
				codes[j] = runApp(t, "connections", "test", "service/grafana").code
			}()
		}
		wg.Wait()
		for j, code := range codes {
			if code != cli.ExitError {
				t.Fatalf("concurrent failing test %d/%d: exit %d", i, j, code)
			}
		}
		if state := connectionState(t, dir, "service/grafana"); state != "failing" {
			t.Fatalf("state after concurrent failing tests %d = %q", i, state)
		}
		if after := connectionNotices(t, dir, "Grafana"); after != before+1 {
			t.Fatalf("%d notices after concurrent failing tests %d, want %d", after-before, i, 1)
		}
	}
}

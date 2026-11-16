//go:build !modernc

package main

import (
	"database/sql"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db/dbtest"
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
	waitFor(t, func() bool { return connectionNotices(t, dir, "Grafana") > 0 })
	if n := connectionNotices(t, dir, "Grafana"); n != 1 || connectionState(t, dir, "service/grafana") != "failing" {
		t.Fatalf("%d notices for a host connection without a token after start, want 1", n)
	}

	if code, body := s.conn("/test", url.Values{"id": {"service/grafana"}, "api_key": {"glsa_good"}}); code != http.StatusOK {
		t.Fatalf("grafana test: %d %s", code, body)
	}
	if state := connectionState(t, dir, "service/grafana"); state != "ok" {
		t.Fatalf("state after a passing test = %q", state)
	}
	if n := connectionNotices(t, dir, "Grafana"); n != 2 {
		t.Fatalf("%d notices after the recovery, want 2", n)
	}

	status.Store(http.StatusInternalServerError)
	if got := runApp(t, "connections", "test", "service/grafana"); got.code != cli.ExitError || !strings.Contains(got.stdout+got.stderr, "failing") {
		t.Fatalf("failing test: exit %d stdout %q stderr %q", got.code, got.stdout, got.stderr)
	}
	if state := connectionState(t, dir, "service/grafana"); state != "failing" {
		t.Fatalf("state after a failing test = %q", state)
	}
	if n := connectionNotices(t, dir, "Grafana"); n != 3 {
		t.Fatalf("%d notices after one transition, want 3", n)
	}
	if got := runApp(t, "connections", "test", "service/grafana"); got.code != cli.ExitError {
		t.Fatalf("second failing test: exit %d", got.code)
	}
	if n := connectionNotices(t, dir, "Grafana"); n != 3 {
		t.Fatalf("%d notices after an unchanged failing check, want 3", n)
	}

	status.Store(http.StatusUnauthorized)
	if got := runApp(t, "connections", "test", "service/grafana"); got.code != cli.ExitError {
		t.Fatalf("refused token: exit %d", got.code)
	}
	if state := connectionState(t, dir, "service/grafana"); state != "needs_reauth" {
		t.Fatalf("state after a refused token = %q", state)
	}
	if n := connectionNotices(t, dir, "Grafana"); n != 4 {
		t.Fatalf("%d notices after a second transition, want 4", n)
	}

	s.stop()
	s.start()
	waitFor(t, func() bool { return connectionState(t, dir, "service/grafana") == "needs_reauth" })
	if n := connectionNotices(t, dir, "Grafana"); n != 4 {
		t.Fatalf("%d notices after a restart in the same state, want 4", n)
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

func TestHostConnectionChangesAreCheckedAtStartAndAnnouncedOnce(t *testing.T) {
	t.Parallel()
	requireCipher(t)
	good, broken := newFakeOpenAI(t), newFakeOpenAI(t)
	broken.setStatus(http.StatusInternalServerError)
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	host := func(embedURL string, withQbit bool) string {
		t.Helper()
		table := fmt.Sprintf(`
[connections."service/embedding"]
kind = "service/embedding"
label = "Embedding"
config = { url = %q, model = "embeddinggemma" }
capabilities = ["embeddings"]
`, embedURL)
		if withQbit {
			table += defaultQbitConnection
		}
		path := filepath.Join(dir, "host-changes.toml")
		if err := os.WriteFile(path, []byte(hostConfigBodyWith(table, defaultWebPushConnection, filepath.Join(testRoot, "host", "anthropic.key"))), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
	checkedAt := func(id string) float64 {
		t.Helper()
		var at sql.NullFloat64
		if err := d.Read.QueryRow("SELECT checked_at FROM connections WHERE id = ?", id).Scan(&at); err != nil {
			t.Fatal(err)
		}
		return at.Float64
	}

	setEnv(t, "ATRIUM_HOST_CONFIG", host(good.URL+"/v1", true))
	if got := runApp(t, "connections", "test", "service/embedding"); got.code != cli.ExitOK || !strings.Contains(got.stdout, "ok") {
		t.Fatalf("passing Test: exit %d stdout %q stderr %q", got.code, got.stdout, got.stderr)
	}
	if n := connectionNotices(t, dir, "Embedding"); n != 0 {
		t.Fatalf("%d notices for a first passing check, want 0", n)
	}

	setEnv(t, "ATRIUM_HOST_CONFIG", host(broken.URL+"/v1", true))
	s := &connectionSite{t: t, dir: dir}
	s.start()
	waitFor(t, func() bool { return titledNotices(t, d, "atrium: Embedding failing") > 0 })
	if state := connectionState(t, dir, "service/embedding"); state != "failing" {
		t.Fatalf("state of a changed host connection after start = %q", state)
	}
	if n := connectionNotices(t, dir, "Embedding"); n != 1 {
		t.Fatalf("%d notices for the first failure of a changed host connection, want 1", n)
	}

	checked := checkedAt("service/embedding")
	s.start()
	waitFor(t, func() bool { return checkedAt("service/embedding") > checked })
	if state := connectionState(t, dir, "service/embedding"); state != "failing" {
		t.Fatalf("state after a restart = %q", state)
	}
	if n := connectionNotices(t, dir, "Embedding"); n != 1 {
		t.Fatalf("%d notices after a second failing check, want 1", n)
	}

	s.stop()
	setEnv(t, "ATRIUM_HOST_CONFIG", host(broken.URL+"/v1", false))
	s.start()
	if state := connectionState(t, dir, "service/qbittorrent"); state != "disabled" {
		t.Fatalf("state of a connection dropped from the host file = %q", state)
	}
	if n := titledNotices(t, d, "atrium: qBittorrent disabled"); n != 1 {
		t.Fatalf("%d notices for a connection dropped from the host file, want 1", n)
	}
	if listed := runApp(t, "connections", "list"); listed.code != cli.ExitOK || !strings.Contains(listed.stdout, "removed from host configuration") {
		t.Fatalf("connections list after the host row was dropped: %d %q", listed.code, listed.stdout)
	}
	s.start()
	defer s.stop()
	if n := titledNotices(t, d, "atrium: qBittorrent disabled"); n != 1 {
		t.Fatalf("%d notices after restarting without the host row, want 1", n)
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

func TestEmbeddingOutageShowsOnMailWithoutHoldingItAndRecoveryClearsIt(t *testing.T) {
	embed := newFakeOpenAI(t)
	dir, api, _ := moduleFixture(t, flagsExcept(module.Mail, ""))
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
	if _, err := d.Write.Exec("UPDATE connections SET checked_at = checked_at - 90000 WHERE id = 'service/embedding'"); err != nil {
		t.Fatal(err)
	}
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
	if degraded := moduleDegraded(t, s, "mail"); degraded != "" {
		t.Fatalf("mail shows a degraded requirement before the outage: %q", degraded)
	}

	embed.setStatus(http.StatusInternalServerError)
	if _, err := d.Write.Exec("UPDATE connections SET checked_at = checked_at - 600 WHERE id = 'service/embedding'"); err != nil {
		t.Fatal(err)
	}
	runApp(t, "reindex")
	waitFor(t, func() bool { return connectionState(t, dir, "service/embedding") == "failing" })
	if n := connectionNotices(t, dir, "Embedding"); n != 1 {
		t.Fatalf("%d notices after the automatic failure, want 1", n)
	}
	if degraded := moduleDegraded(t, s, "mail"); !strings.Contains(degraded, "service/embedding is failing") {
		t.Fatalf("mail requirement during the outage: %q", degraded)
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
	if degraded := moduleDegraded(t, s, "mail"); degraded != "" {
		t.Fatalf("mail requirement after recovery: %q", degraded)
	}
	if n := connectionNotices(t, dir, "Embedding"); n != 2 {
		t.Fatalf("%d notices after one outage and one recovery, want 2", n)
	}
}

func moduleDegraded(t *testing.T, s *connectionSite, page string) string {
	t.Helper()
	code, body, _ := fetch(t, s.client, http.MethodGet, s.base+"/settings/"+page, nil)
	if code != http.StatusOK {
		t.Fatalf("%s settings page: %d", page, code)
	}
	m := regexp.MustCompile(`data-degraded>([^<]*)<`).FindStringSubmatch(body)
	if m == nil {
		return ""
	}
	return html.UnescapeString(m[1])
}

func TestAFailedEmbeddingTestDoesNotHoldMailAndAnAutomaticPassClearsIt(t *testing.T) {
	t.Parallel()
	requireCipher(t)
	embed := newFakeOpenAI(t)
	dir, _, _ := moduleFixture(t, flagsExcept(module.Mail, ""))
	setEnv(t, "ATRIUM_HOST_CONFIG", embeddingHostWith(t, dir, embed.URL+"/v1", "embeddinggemma", "", ""))

	embed.setStatus(http.StatusInternalServerError)
	if got := runApp(t, "connections", "test", "service/embedding"); got.code != cli.ExitError {
		t.Fatalf("failing Test: exit %d stdout %q stderr %q", got.code, got.stdout, got.stderr)
	}
	if on := runSettings(t, "set", module.FlagKey(module.Mail), "true"); on.code != cli.ExitOK {
		t.Fatalf("mail held by a failed Test: %d %q", on.code, on.stderr)
	}

	embed.setStatus(0)
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
	if _, err := d.Write.Exec("UPDATE connections SET checked_at = checked_at - 90000 WHERE id = 'service/embedding'"); err != nil {
		t.Fatal(err)
	}
	s := &connectionSite{t: t, dir: dir}
	s.start()
	defer s.stop()
	waitFor(t, func() bool { return connectionState(t, dir, "service/embedding") == "ok" })
	if degraded := moduleDegraded(t, s, "mail"); degraded != "" {
		t.Fatalf("mail requirement after an automatic pass: %q", degraded)
	}
}

func TestUnconfiguredConnectionsShowAsUnconfiguredOnTheirModules(t *testing.T) {
	t.Parallel()
	requireCipher(t)
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	setEnv(t, "ATRIUM_DB_KEY", rightKeyHex)
	setEnv(t, "ATRIUM_HOST_CONFIG", servicesHostConfig(t, dir, "http://grafana.invalid", "http://anthropic.invalid"))

	if got := runApp(t, "connections", "test", "service/anthropic"); got.code == cli.ExitOK || !strings.Contains(got.stdout+got.stderr, "unconfigured") {
		t.Fatalf("anthropic test without a key: exit %d stdout %q stderr %q", got.code, got.stdout, got.stderr)
	}
	if state := connectionState(t, dir, "service/anthropic"); state != "unconfigured" {
		t.Fatalf("anthropic state without a key = %q, want unconfigured", state)
	}

	s := &connectionSite{t: t, dir: dir}
	s.start()
	defer s.stop()
	if degraded := moduleDegraded(t, s, "assistant"); !strings.Contains(degraded, "service/anthropic is unconfigured") {
		t.Fatalf("assistant requirement without a key: %q", degraded)
	}
	moduleDegraded(t, s, "notifications")
	if got := runApp(t, "connections", "test", "service/webpush"); got.code == cli.ExitOK {
		t.Fatalf("web push test without a subject: exit %d stdout %q", got.code, got.stdout)
	}
	if state := connectionState(t, dir, "service/webpush"); state != "unconfigured" {
		t.Fatalf("web push state without a subject = %q, want unconfigured", state)
	}
	if degraded := moduleDegraded(t, s, "notifications"); !strings.Contains(degraded, "service/webpush is unconfigured") {
		t.Fatalf("notifications requirement without a subject: %q", degraded)
	}
	if on := runSettings(t, "set", module.FlagKey(module.Notifications), "true"); on.code != cli.ExitOK {
		t.Fatalf("an unconfigured connection held notifications: %d %q", on.code, on.stderr)
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
	waitFor(t, func() bool { return connectionNotices(t, dir, "Grafana") > 0 })
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

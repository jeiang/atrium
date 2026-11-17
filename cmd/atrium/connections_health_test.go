package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db/dbtest"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/unit"
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

	okG3(t, "grafana test", connTestG3(t, s, "service/grafana", "", map[string]string{"api_key": "glsa_good"}))
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
	connDisconnectG3(t, s, "service/grafana")
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

	s.stop()
	if _, err := d.Write.Exec("UPDATE connections SET checked_at = checked_at - 3700 WHERE id = 'service/embedding'"); err != nil {
		t.Fatal(err)
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
	if listed := runApp(t, "connections", "list"); listed.code != cli.ExitOK || !strings.Contains(listed.stdout, "removed from host configuration") {
		t.Fatalf("connections list after the host row was dropped: %d %q", listed.code, listed.stdout)
	}
	if state := connectionState(t, dir, "service/qbittorrent"); state != "disabled" {
		t.Fatalf("state of a connection dropped from the host file = %q", state)
	}
	if n := titledNotices(t, d, "atrium: qBittorrent disabled"); n != 0 {
		t.Fatalf("%d notices from a CLI process that reconciled the dropped host row, want 0", n)
	}
	s.start()
	waitFor(t, func() bool { return titledNotices(t, d, "atrium: qBittorrent disabled") > 0 })
	if n := titledNotices(t, d, "atrium: qBittorrent disabled"); n != 1 {
		t.Fatalf("%d notices for a connection dropped from the host file, want 1", n)
	}
	s.stop()
	if _, err := d.Write.Exec("UPDATE connections SET checked_at = checked_at - 3700 WHERE id = 'service/embedding'"); err != nil {
		t.Fatal(err)
	}
	checked = checkedAt("service/embedding")
	s.start()
	defer s.stop()
	waitFor(t, func() bool { return checkedAt("service/embedding") > checked })
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
	okG3(t, "grafana test", connTestG3(t, s, "service/grafana", "", map[string]string{"api_key": "glsa_good"}))
	scrape := func() string {
		code, body, _ := fetch(t, http.DefaultClient, http.MethodGet, ops()+"/metrics", nil)
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
	t.Parallel()
	embed := newFakeOpenAI(t)
	dir, api, _ := moduleFixture(t, flagsExcept(unit.Mail, ""))
	setEnv(t, "ATRIUM_HOST_CONFIG", embeddingHostWith(t, dir, embed.URL+"/v1", "embeddinggemma", "", ""))

	if got := runApp(t, "connections", "test", "service/embedding"); got.code != cli.ExitOK || !strings.Contains(got.stdout, "ok") {
		t.Fatalf("passing Test: exit %d stdout %q stderr %q", got.code, got.stdout, got.stderr)
	}
	if on := runSettings(t, "set", module.FlagKey(unit.Mail), "true"); on.code != cli.ExitOK {
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
	if state := needStateG3(t, s.base, "mail", "service/embedding"); state != "ok" {
		t.Fatalf("mail need on embedding before the outage: %q", state)
	}

	embed.setStatus(http.StatusInternalServerError)
	s.stop()
	// Request outcomes reach only the serving app's health watch, so the
	// automatic failure comes from the start sweep of a row last checked a day
	// ago, still inside the grace window of its last success.
	if _, err := d.Write.Exec("UPDATE connections SET checked_at = checked_at - 90000 WHERE id = 'service/embedding'"); err != nil {
		t.Fatal(err)
	}
	s.start()
	waitFor(t, func() bool { return connectionState(t, dir, "service/embedding") == "failing" })
	if n := connectionNotices(t, dir, "Embedding"); n != 0 {
		t.Fatalf("%d notices after an automatic failure inside the grace window, want 0", n)
	}
	if state := needStateG3(t, s.base, "mail", "service/embedding"); state != "away" {
		t.Fatalf("mail need on embedding during the outage: %q", state)
	}
	if view := connViewG3(t, s.base, "service/embedding"); view["grace_until"] == nil {
		t.Fatalf("embedding during the outage has no grace window: %v", view)
	}
	if on := runSettings(t, "set", module.FlagKey(unit.Mail), "true"); on.code != cli.ExitOK {
		t.Fatalf("mail held by an automatic failure: %d %q", on.code, on.stderr)
	}
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("hi")))
	if names := offeredTools(t, api, runApp(t, "agent", "run", "--text", "hello", "--kind", "chat")); !slices.Contains(names, "mail_search") {
		t.Fatalf("mail tools during the outage: %v", names)
	}
	if listed := runApp(t, "connections", "list"); listed.code != cli.ExitOK || !strings.Contains(listed.stdout, "service/embedding") || !strings.Contains(listed.stdout, "away until ") {
		t.Fatalf("connections list during the outage: %d %q", listed.code, listed.stdout)
	}

	embed.setStatus(0)
	s.stop()
	if _, err := d.Write.Exec("UPDATE connections SET checked_at = checked_at - 3700 WHERE id = 'service/embedding'"); err != nil {
		t.Fatal(err)
	}
	s.start()
	waitFor(t, func() bool { return connectionState(t, dir, "service/embedding") == "ok" })
	if state := needStateG3(t, s.base, "mail", "service/embedding"); state != "ok" {
		t.Fatalf("mail need on embedding after recovery: %q", state)
	}
	if n := connectionNotices(t, dir, "Embedding"); n != 0 {
		t.Fatalf("%d notices after an outage that recovered inside the grace window, want 0", n)
	}
}

func TestAFailedEmbeddingTestDoesNotHoldMailAndAnAutomaticPassClearsIt(t *testing.T) {
	t.Parallel()
	requireCipher(t)
	embed := newFakeOpenAI(t)
	dir, _, _ := moduleFixture(t, flagsExcept(unit.Mail, ""))
	setEnv(t, "ATRIUM_HOST_CONFIG", embeddingHostWith(t, dir, embed.URL+"/v1", "embeddinggemma", "", ""))

	embed.setStatus(http.StatusInternalServerError)
	if got := runApp(t, "connections", "test", "service/embedding"); got.code != cli.ExitError {
		t.Fatalf("failing Test: exit %d stdout %q stderr %q", got.code, got.stdout, got.stderr)
	}
	if on := runSettings(t, "set", module.FlagKey(unit.Mail), "true"); on.code != cli.ExitOK {
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
	if state := needStateG3(t, s.base, "mail", "service/embedding"); state != "ok" {
		t.Fatalf("mail need on embedding after an automatic pass: %q", state)
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
	if state := needStateG3(t, s.base, "assistant", "service/anthropic"); state != "unconfigured" {
		t.Fatalf("assistant need without a key: %q", state)
	}
	if got := runApp(t, "connections", "test", "service/webpush"); got.code == cli.ExitOK {
		t.Fatalf("web push test without a subject: exit %d stdout %q", got.code, got.stdout)
	}
	if state := connectionState(t, dir, "service/webpush"); state != "unconfigured" {
		t.Fatalf("web push state without a subject = %q, want unconfigured", state)
	}
	if state := needStateG3(t, s.base, "notifications", "service/webpush"); state != "unconfigured" {
		t.Fatalf("notifications need without a subject: %q", state)
	}
	if on := runSettings(t, "set", module.FlagKey(unit.Notifications), "true"); on.code != cli.ExitOK {
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
	okG3(t, "grafana test", connTestG3(t, s, "service/grafana", "", map[string]string{"api_key": "glsa_good"}))

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

type listedConnection struct {
	ID         string     `json:"id"`
	State      string     `json:"state"`
	GraceUntil *time.Time `json:"grace_until"`
}

func listedEmbedding(t *testing.T) listedConnection {
	t.Helper()
	got := runApp(t, "connections", "list", "--json")
	if got.code != cli.ExitOK {
		t.Fatalf("connections list --json: %d %q", got.code, got.stderr)
	}
	var views []listedConnection
	if err := json.Unmarshal([]byte(got.stdout), &views); err != nil {
		t.Fatalf("connections list --json: %v\n%s", err, got.stdout)
	}
	for _, v := range views {
		if v.ID == "service/embedding" {
			return v
		}
	}
	t.Fatalf("no service/embedding in %s", got.stdout)
	return listedConnection{}
}

func TestGraceWindowKeepsAnEmbeddingOutageQuietUntilTheWindowEnds(t *testing.T) {
	t.Parallel()
	embed := newFakeOpenAI(t)
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	setEnv(t, "ATRIUM_HOST_CONFIG", embeddingHostWith(t, dir, embed.URL+"/v1", "embeddinggemma", "", ""))
	if got := runApp(t, "connections", "test", "service/embedding"); got.code != cli.ExitOK {
		t.Fatalf("passing test: %d %q %q", got.code, got.stdout, got.stderr)
	}

	embed.setStatus(http.StatusInternalServerError)
	before := time.Now()
	got := runApp(t, "connections", "test", "service/embedding")
	after := time.Now()
	if got.code != cli.ExitError || !strings.Contains(got.stdout, "service/embedding\taway until ") {
		t.Fatalf("failing test inside the grace window: %d %q %q", got.code, got.stdout, got.stderr)
	}
	listed := listedEmbedding(t)
	if listed.State != "failing" || listed.GraceUntil == nil ||
		listed.GraceUntil.Before(before.Add(6*time.Hour-time.Second)) || listed.GraceUntil.After(after.Add(6*time.Hour+time.Second)) {
		t.Fatalf("embedding inside the grace window: %+v", listed)
	}
	until := "away until " + listed.GraceUntil.UTC().Format("2006-01-02 15:04 UTC")
	if !strings.Contains(got.stdout, until) {
		t.Fatalf("test output %q lacks %q", got.stdout, until)
	}
	if n := connectionNotices(t, dir, "Embedding"); n != 0 {
		t.Fatalf("%d notices inside the grace window, want 0", n)
	}
	s := &connectionSite{t: t, dir: dir}
	s.start()
	if view := connViewG3(t, s.base, "service/embedding"); view["state"] != "failing" || view["grace_until"] == nil {
		t.Fatalf("connections read lacks the grace window: %v", view)
	} else if shown, err := time.Parse(time.RFC3339Nano, view["grace_until"].(string)); err != nil || !shown.Equal(*listed.GraceUntil) {
		t.Fatalf("connections read grace_until %v, want %v: %v", view["grace_until"], listed.GraceUntil, err)
	}
	s.stop()

	d := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
	if _, err := d.Write.Exec("UPDATE connections SET results_json = json_set(results_json, '$[0].grace_until', ?), checked_at = checked_at - 30000 WHERE id = 'service/embedding'",
		time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if got := runApp(t, "connections", "test", "service/embedding"); got.code != cli.ExitError || !strings.Contains(got.stdout, "service/embedding\tfailing") {
		t.Fatalf("failing test after the grace window: %d %q %q", got.code, got.stdout, got.stderr)
	}
	if listed := listedEmbedding(t); listed.State != "failing" || listed.GraceUntil != nil {
		t.Fatalf("embedding after the grace window: %+v", listed)
	}
	if n := titledNotices(t, d, "atrium: Embedding failing"); n != 1 {
		t.Fatalf("%d failing notices after the grace window, want 1", n)
	}
	if got := runApp(t, "connections", "test", "service/embedding"); got.code != cli.ExitError {
		t.Fatalf("second failing test: %d %q", got.code, got.stdout)
	}
	if n := connectionNotices(t, dir, "Embedding"); n != 1 {
		t.Fatalf("%d notices after a repeated failure, want 1", n)
	}

	embed.setStatus(0)
	if got := runApp(t, "connections", "test", "service/embedding"); got.code != cli.ExitOK || !strings.Contains(got.stdout, "service/embedding\tok") {
		t.Fatalf("recovered test: %d %q %q", got.code, got.stdout, got.stderr)
	}
	if n := connectionNotices(t, dir, "Embedding"); n != 2 {
		t.Fatalf("%d notices after a failure past the window and a recovery, want 2", n)
	}
}

func countingGrafana(t *testing.T, status int) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		switch {
		case status != http.StatusOK:
			w.WriteHeader(status)
		case r.URL.Path == "/api/user":
			_, _ = w.Write([]byte(`{"login":"sa-atrium"}`))
		case r.URL.Path == "/api/search":
			_, _ = w.Write([]byte(`[]`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func TestConnectionsCheckChangedChecksOnlyWhatTheHostFileChanged(t *testing.T) {
	t.Parallel()
	embed := newFakeOpenAI(t)
	healthy, healthyHits := countingGrafana(t, http.StatusOK)
	broken, brokenHits := countingGrafana(t, http.StatusInternalServerError)
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	host := filepath.Join(dir, "host-check.toml")
	embedding := fmt.Sprintf(`
[connections."service/embedding"]
kind = "service/embedding"
label = "Embedding"
config = { url = %q, model = "embeddinggemma" }
capabilities = ["embeddings"]
`, embed.URL+"/v1")
	writeHost := func(grafana string) {
		body := strings.Replace(hostConfigBodyWith(embedding, defaultWebPushConnection, ""), "http://grafana.invalid", grafana, 1)
		if err := os.WriteFile(host, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	embedHits := func() int {
		n := 0
		for _, values := range embed.authorizations() {
			n += len(values)
		}
		return n
	}
	writeHost(healthy.URL)
	setEnv(t, "ATRIUM_HOST_CONFIG", host)

	first := runApp(t, "connections", "check", "--changed")
	if !strings.Contains(first.stdout, "service/embedding\tok") || !strings.Contains(first.stdout, "service/grafana\tok") {
		t.Fatalf("first changed check: %d %q %q", first.code, first.stdout, first.stderr)
	}
	embedded, grafana := embedHits(), healthyHits.Load()
	if embedded == 0 || grafana == 0 {
		t.Fatalf("first changed check reached embedding %d and grafana %d times", embedded, grafana)
	}

	if got := runApp(t, "connections", "check", "--changed"); got.code != cli.ExitOK || strings.TrimSpace(got.stdout) != "no connection to check" {
		t.Fatalf("changed check with nothing changed: %d %q %q", got.code, got.stdout, got.stderr)
	}
	if embedHits() != embedded || healthyHits.Load() != grafana {
		t.Fatal("changed check with nothing changed reached a fake service")
	}

	writeHost(broken.URL)
	got := runApp(t, "connections", "check", "--changed")
	if got.code != cli.ExitError || !strings.HasPrefix(got.stdout, "service/grafana\tfailing\t") {
		t.Fatalf("changed check after a failing edit: %d %q %q", got.code, got.stdout, got.stderr)
	}
	if strings.Contains(got.stdout, "service/embedding") || embedHits() != embedded || healthyHits.Load() != grafana || brokenHits.Load() == 0 {
		t.Fatalf("changed check after one edit touched more than the edited connection: %q", got.stdout)
	}

	writeHost(healthy.URL)
	if got := runApp(t, "connections", "check", "--changed"); got.code != cli.ExitOK || !strings.HasPrefix(got.stdout, "service/grafana\tok") {
		t.Fatalf("changed check after a fixing edit: %d %q %q", got.code, got.stdout, got.stderr)
	}
	if embedHits() != embedded {
		t.Fatal("changed check after a fixing edit reached the unchanged embedding service")
	}
}

func TestHostCredentialFileEditsResetTheConnectionAndARedeclaredConnectionReturns(t *testing.T) {
	t.Parallel()
	embed := newFakeOpenAI(t)
	grafana, _ := countingGrafana(t, http.StatusOK)
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	host := filepath.Join(dir, "host-credentials.toml")
	first, second := filepath.Join(dir, "embedding-first.key"), filepath.Join(dir, "embedding-second.key")
	for path, key := range map[string]string{first: "first-key", second: "second-key"} {
		if err := os.WriteFile(path, []byte(key+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeHost := func(declared bool, keyFile string) {
		embedding := ""
		if declared {
			embedding = fmt.Sprintf(`
[connections."service/embedding"]
kind = "service/embedding"
label = "Embedding"
config = { url = %q, model = "embeddinggemma" }
capabilities = ["embeddings"]
`, embed.URL+"/v1")
			if keyFile != "" {
				embedding += fmt.Sprintf("credential_files = { bearer = %q }\n", keyFile)
			}
		}
		body := strings.Replace(hostConfigBodyWith(embedding, defaultWebPushConnection, ""), "http://grafana.invalid", grafana.URL, 1)
		if err := os.WriteFile(host, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	type row struct {
		enabled int
		state   string
		checked sql.NullFloat64
		results string
		ref     sql.NullString
	}
	read := func() row {
		t.Helper()
		d := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
		defer func() { _ = d.Close() }()
		var r row
		if err := d.Read.QueryRow("SELECT enabled, state, checked_at, results_json, "+
			"(SELECT file_ref FROM connection_credentials WHERE connection_id = c.id AND slot = 'bearer') "+
			"FROM connections c WHERE id = 'service/embedding'").Scan(&r.enabled, &r.state, &r.checked, &r.results, &r.ref); err != nil {
			t.Fatal(err)
		}
		return r
	}
	reconcile := func(step string) row {
		t.Helper()
		if got := runApp(t, "connections", "list"); got.code != cli.ExitOK {
			t.Fatalf("%s: connections list: %d %q", step, got.code, got.stderr)
		}
		return read()
	}
	untested := func(step string, r row, ref string) {
		t.Helper()
		if r.enabled != 1 || r.state != "untested" || r.checked.Valid || r.results != "{}" || r.ref.String != ref {
			t.Fatalf("%s: row %+v, want enabled untested with no check and reference %q", step, r, ref)
		}
	}
	checksEmbedding := func(step, key string) {
		t.Helper()
		before := embed.authorizations()
		got := runApp(t, "connections", "check", "--changed")
		if got.code != cli.ExitOK || !strings.HasPrefix(got.stdout, "service/embedding\tok\t") || strings.Count(got.stdout, "\n") != 1 {
			t.Fatalf("%s: changed check: %d %q %q", step, got.code, got.stdout, got.stderr)
		}
		want := ""
		if key != "" {
			want = "Bearer " + key
		}
		var sent []string
		for path, values := range embed.authorizations() {
			sent = append(sent, values[len(before[path]):]...)
		}
		if len(sent) == 0 || slices.ContainsFunc(sent, func(v string) bool { return v != want }) {
			t.Fatalf("%s: changed check sent %q to the embedding service, want only %q", step, sent, want)
		}
		if r := read(); r.state != "ok" || !r.checked.Valid {
			t.Fatalf("%s: row after the changed check %+v", step, r)
		}
	}
	setEnv(t, "ATRIUM_HOST_CONFIG", host)

	writeHost(true, "")
	if got := runApp(t, "connections", "check", "--changed"); !strings.Contains(got.stdout, "service/embedding\tok") {
		t.Fatalf("first changed check: %d %q %q", got.code, got.stdout, got.stderr)
	}
	if r := read(); r.state != "ok" || !r.checked.Valid || r.ref.Valid {
		t.Fatalf("row before the slot was declared: %+v", r)
	}

	writeHost(true, first)
	untested("slot added", reconcile("slot added"), first)
	checksEmbedding("slot added", "first-key")

	writeHost(true, second)
	untested("path changed", reconcile("path changed"), second)
	checksEmbedding("path changed", "second-key")

	settled := read()
	for _, step := range []string{"first unchanged restart", "second unchanged restart"} {
		if r := reconcile(step); r != settled {
			t.Fatalf("%s reset the row: %+v, was %+v", step, r, settled)
		}
	}
	if got := runApp(t, "connections", "check", "--changed"); got.code != cli.ExitOK || strings.TrimSpace(got.stdout) != "no connection to check" {
		t.Fatalf("changed check after an unchanged restart: %d %q %q", got.code, got.stdout, got.stderr)
	}
	if r := read(); r != settled {
		t.Fatalf("changed check after an unchanged restart touched the row: %+v, was %+v", r, settled)
	}

	writeHost(true, "")
	untested("slot removed", reconcile("slot removed"), "")
	checksEmbedding("slot removed", "")

	writeHost(false, "")
	if r := reconcile("connection removed"); r.enabled != 0 || r.state != "disabled" {
		t.Fatalf("row after the connection was removed from the host file: %+v", r)
	}
	writeHost(true, second)
	untested("connection declared again", reconcile("connection declared again"), second)
	checksEmbedding("connection declared again", "second-key")

	s := &connectionSite{t: t, dir: dir}
	s.start()
	connDisconnectG3(t, s, "service/embedding")
	s.stop()
	if r := read(); r.enabled != 0 || r.state != "unconfigured" {
		t.Fatalf("row after the operator disconnected it: %+v", r)
	}
	writeHost(false, "")
	if r := reconcile("disconnected connection removed"); r.enabled != 0 || r.state == "disabled" {
		t.Fatalf("host removal overwrote the operator's disconnect: %+v", r)
	}
	writeHost(true, second)
	if r := reconcile("disconnected connection declared again"); r.enabled != 0 {
		t.Fatalf("declaring a disconnected connection again enabled it: %+v", r)
	}
}

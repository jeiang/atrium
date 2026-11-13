package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/proposals"
)

func localAPIToken(t *testing.T, name string) string {
	t.Helper()
	issued := runApp(t, "auth", "token", "issue", "--name", name)
	if issued.code != cli.ExitOK {
		t.Fatalf("auth token issue: exit %d stdout %q stderr %q", issued.code, issued.stdout, issued.stderr)
	}
	return strings.TrimSpace(issued.stdout)
}

func commandStatus(t *testing.T, s *apiSite, token, id string) map[string]any {
	t.Helper()
	got := s.do(http.MethodGet, "/api/v1/commands/"+id, token, nil)
	data, _ := got.json["data"].(map[string]any)
	if got.status != http.StatusOK || data["command_id"] != id {
		t.Fatalf("command %s: %d %s", id, got.status, got.body)
	}
	return data
}

func TestCommandKeyReplaysAcrossRestartAndBearers(t *testing.T) {
	dir, _, f := moduleFixture(t, flagsExcept("", ""))
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), db.Key{})
	defer func() { _ = d.Close() }()
	threads := asstCount(t, d, "SELECT COUNT(*) FROM threads")
	first := localAPIToken(t, "first")
	base, _, stop, err := startServeAppArgs(t, f.app, &lockedBuffer{}, "--auth", "local")
	if err != nil {
		t.Fatal(err)
	}
	s := &apiSite{t: t, base: base}
	body := map[string]any{"web_search": false}
	created := s.do(http.MethodPost, "/api/v1/threads", first, body, "Idempotency-Key", "thread-1")
	command := created.header.Get("Atrium-Command-Id")
	if created.status != http.StatusCreated || command == "" || created.header.Get("Idempotent-Replayed") != "" {
		t.Fatalf("create thread: %d %s %v", created.status, created.body, created.header)
	}
	if data := commandStatus(t, s, first, command); data["state"] != "succeeded" || data["status"] != float64(http.StatusCreated) || data["ended_at"] == nil {
		t.Fatalf("command after create: %v", data)
	}
	stop()

	second := localAPIToken(t, "second")
	if got := runApp(t, "auth", "token", "revoke", strconv.Itoa(asstCount(t, d, "SELECT id FROM auth_bearers WHERE name = 'first'"))); got.code != cli.ExitOK {
		t.Fatalf("auth token revoke: %d %q %q", got.code, got.stdout, got.stderr)
	}
	base, _, stop, err = startServeAppArgs(t, f.app, &lockedBuffer{}, "--auth", "local")
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	s = &apiSite{t: t, base: base}
	expectAPI(t, s.do(http.MethodPost, "/api/v1/threads", first, body, "Idempotency-Key", "thread-1"), http.StatusUnauthorized, "invalid_token")
	replayed := s.do(http.MethodPost, "/api/v1/threads", second, body, "Idempotency-Key", "thread-1")
	if replayed.status != created.status || replayed.body != created.body || replayed.header.Get("Idempotent-Replayed") != "true" ||
		replayed.header.Get("Atrium-Command-Id") != command || replayed.header.Get("Content-Type") != created.header.Get("Content-Type") {
		t.Fatalf("replay: %d %s %v, want %d %s", replayed.status, replayed.body, replayed.header, created.status, created.body)
	}
	if n := asstCount(t, d, "SELECT COUNT(*) FROM threads"); n != threads+1 {
		t.Fatalf("%d threads after a replay, want %d", n, threads+1)
	}
	expectAPI(t, s.do(http.MethodPost, "/api/v1/threads", second, map[string]any{"web_search": true}, "Idempotency-Key", "thread-1"), http.StatusUnprocessableEntity, "idempotency_mismatch")
	expectAPI(t, s.do(http.MethodPost, "/api/v1/threads", second, body, "Idempotency-Key", strings.Repeat("k", 256)), http.StatusUnprocessableEntity, "invalid")
	if n := asstCount(t, d, "SELECT COUNT(*) FROM commands WHERE idempotency_key = 'thread-1'"); n != 1 {
		t.Fatalf("%d command rows for one key", n)
	}
	var stored string
	if err := d.Read.QueryRow("SELECT CAST(body AS TEXT) FROM commands WHERE id = ? AND state = 'succeeded' AND status = 201 AND withheld = 0", command).Scan(&stored); err != nil || stored != created.body {
		t.Fatalf("stored response %q (%v), want %q", stored, err, created.body)
	}
	expectAPI(t, s.do(http.MethodGet, "/api/v1/commands/cmd_missing", second, nil), http.StatusNotFound, "not_found")
}

func TestUnkeyedCommandStatusShowsAudit(t *testing.T) {
	dir, _, f := moduleFixture(t, flagsExcept("", ""))
	d := asstDB(t, dir)
	id, _, err := proposals.Create(testProposalContext(), d, "rebalance_split", nil, "deny me", map[string]any{}, map[string]any{}, "deny me", float64(time.Now().UnixNano())/1e9)
	if err != nil {
		t.Fatal(err)
	}
	token := localAPIToken(t, "phone")
	base, _, stop, err := startServeAppArgs(t, f.app, &lockedBuffer{}, "--auth", "local")
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	s := &apiSite{t: t, base: base}
	denied := s.do(http.MethodPost, "/api/v1/approvals/"+strconv.FormatInt(id, 10)+"/deny", token, nil)
	command := denied.header.Get("Atrium-Command-Id")
	if denied.status != http.StatusOK || command == "" {
		t.Fatalf("deny: %d %s %v", denied.status, denied.body, denied.header)
	}
	data := commandStatus(t, s, token, command)
	audit, _ := data["audit"].([]any)
	effects, _ := data["effect_ids"].([]any)
	if data["state"] != "succeeded" || data["status"] != float64(http.StatusOK) || audit == nil || effects == nil {
		t.Fatalf("command after deny: %v", data)
	}
	if n := asstCount(t, d, "SELECT COUNT(*) FROM commands WHERE id = ? AND idempotency_key IS NULL AND method = 'POST' AND pattern = '/api/v1/approvals/{proposal_id}/deny'", command); n != 1 {
		t.Fatalf("command row for the deny: %d", n)
	}
	again := s.do(http.MethodPost, "/api/v1/approvals/"+strconv.FormatInt(id, 10)+"/deny", token, nil)
	if next := again.header.Get("Atrium-Command-Id"); next == "" || next == command || again.header.Get("Idempotent-Replayed") != "" {
		t.Fatalf("second unkeyed deny: %d %v", again.status, again.header)
	}
	if got := s.do(http.MethodGet, "/api/v1/me", token, nil); got.header.Get("Atrium-Command-Id") != "" {
		t.Fatalf("a read got a command id: %v", got.header)
	}
}

func TestInterruptedCommandNeverReruns(t *testing.T) {
	const hash = "dddddddddddddddddddddddddddddddddddddddd"
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	mnpMediaRoot(t, dir)
	qb, _ := mnpNewQbit(t)
	var mu sync.Mutex
	adds := 0
	held := make(chan struct{})
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/torrents/add") {
			mu.Lock()
			adds++
			first := adds == 1
			mu.Unlock()
			if first {
				close(held)
				<-release
			}
		}
		qb.handle(w, r)
	}))
	t.Cleanup(srv.Close)
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	setEnv(t, "ATRIUM_HOST_CONFIG", mediaHost(t, dir, srv.URL, "", "", ""))
	id := proposalID(t, toolResult(t, "media_add_magnet", map[string]any{"magnet": mediaMagnet(hash, "Lanterns.S01E11")}))
	token := localAPIToken(t, "phone")
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), db.Key{})
	defer func() { _ = d.Close() }()

	p := serveCLIProcess(t, dir)
	s := &apiSite{t: t, base: "http://" + p.addr}
	approve := "/api/v1/approvals/" + id + "/approve"
	go func() {
		req, _ := http.NewRequest(http.MethodPost, s.base+approve, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Idempotency-Key", "add-1")
		if resp, err := http.DefaultClient.Do(req); err == nil {
			_ = resp.Body.Close()
		}
	}()
	select {
	case <-held:
	case <-time.After(60 * time.Second):
		t.Fatalf("the approval never reached qBittorrent: %s", p.stderr.String())
	}
	var command string
	if err := d.Read.QueryRow("SELECT id FROM commands WHERE idempotency_key = 'add-1' AND state = 'running'").Scan(&command); err != nil {
		t.Fatalf("no running command for the held approval: %v", err)
	}
	p.stop(t, syscall.SIGKILL)
	unblock()
	if err := os.Remove(filepath.Join(dir, "serve-addr")); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.Exec("UPDATE effects SET lease_at = lease_at - 3600 WHERE state = 'intent'"); err != nil {
		t.Fatal(err)
	}

	p = serveCLIProcess(t, dir)
	s = &apiSite{t: t, base: "http://" + p.addr}
	if data := commandStatus(t, s, token, command); data["state"] != "interrupted" || data["status"] != nil || data["ended_at"] == nil {
		t.Fatalf("command after the crash: %v", data)
	}
	replayed := s.do(http.MethodPost, approve, token, nil, "Idempotency-Key", "add-1")
	expectAPI(t, replayed, http.StatusConflict, "command_interrupted")
	if e, _ := replayed.json["error"].(map[string]any); e["command_id"] != command || replayed.header.Get("Atrium-Command-Id") != command {
		t.Fatalf("interrupted replay: %s %v", replayed.body, replayed.header)
	}
	mu.Lock()
	n := adds
	mu.Unlock()
	if n != 1 {
		t.Fatalf("qBittorrent received %d adds, want 1", n)
	}
	effects := s.do(http.MethodGet, "/api/v1/effects", token, nil)
	if effects.status != http.StatusOK || !strings.Contains(effects.body, `"state":"unknown"`) {
		t.Fatalf("effects after the crash: %d %s", effects.status, effects.body)
	}
	if got := s.do(http.MethodPost, approve, token, nil); got.status < 400 {
		t.Fatalf("an unkeyed approval after the crash ran again: %d %s", got.status, got.body)
	}
	mu.Lock()
	n = adds
	mu.Unlock()
	if n != 1 {
		t.Fatalf("qBittorrent received %d adds after the unkeyed retry, want 1", n)
	}
}

package main

import (
	"io"
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
	if data["state"] != "succeeded" || data["status"] != float64(http.StatusOK) || len(audit) != 1 || effects == nil {
		t.Fatalf("command after deny: %v", data)
	}
	entry, _ := audit[0].(map[string]any)
	if entry["operation"] != "review.dismiss" {
		t.Fatalf("deny audit: %v", audit)
	}
	if n := asstCount(t, d, "SELECT COUNT(*) FROM operation_audit WHERE command_id = ? AND operation = 'review.dismiss' AND outcome = 'ok'", command); n != 1 {
		t.Fatalf("linked deny audit rows: %d", n)
	}
	if n := asstCount(t, d, "SELECT COUNT(*) FROM commands WHERE id = ? AND idempotency_key IS NULL AND fingerprint = '' AND body IS NULL AND headers_json IS NULL AND status = 200 AND method = 'POST' AND pattern = '/api/v1/approvals/{proposal_id}/deny'", command); n != 1 {
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

func TestLiveCommandOutlivesARecoveryTick(t *testing.T) {
	const hash = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
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
	id := proposalID(t, toolResult(t, "media_add_magnet", map[string]any{"magnet": mediaMagnet(hash, "Lanterns.S01E12")}))
	token := localAPIToken(t, "phone")
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), db.Key{})
	defer func() { _ = d.Close() }()

	p := serveCLIProcess(t, dir)
	s := &apiSite{t: t, base: "http://" + p.addr}
	approve := "/api/v1/approvals/" + id + "/approve"
	type answer struct {
		status int
		body   string
		header http.Header
	}
	first := make(chan answer, 1)
	go func() {
		req, _ := http.NewRequest(http.MethodPost, s.base+approve, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Idempotency-Key", "tick-1")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			first <- answer{}
			return
		}
		defer func() { _ = resp.Body.Close() }()
		raw, _ := io.ReadAll(resp.Body)
		first <- answer{resp.StatusCode, string(raw), resp.Header}
	}()
	select {
	case <-held:
	case <-time.After(60 * time.Second):
		t.Fatalf("the approval never reached qBittorrent: %s", p.stderr.String())
	}
	expectAPI(t, s.do(http.MethodPost, approve, token, nil, "Idempotency-Key", "tick-1"), http.StatusConflict, "idempotency_in_progress")
	unblock()
	var got answer
	select {
	case got = <-first:
	case <-time.After(60 * time.Second):
		t.Fatalf("the held approval never answered: %s", p.stderr.String())
	}
	command := got.header.Get("Atrium-Command-Id")
	if got.status != http.StatusOK || command == "" {
		t.Fatalf("held approval: %d %s %v", got.status, got.body, got.header)
	}
	if data := commandStatus(t, s, token, command); data["state"] != "succeeded" || data["status"] != float64(http.StatusOK) {
		t.Fatalf("command after the held approval: %v", data)
	}
	replayed := s.do(http.MethodPost, approve, token, nil, "Idempotency-Key", "tick-1")
	if replayed.status != http.StatusOK || replayed.body != got.body || replayed.header.Get("Idempotent-Replayed") != "true" || replayed.header.Get("Atrium-Command-Id") != command {
		t.Fatalf("replay after the held approval: %d %s %v, want %s", replayed.status, replayed.body, replayed.header, got.body)
	}
	mu.Lock()
	n := adds
	mu.Unlock()
	if n != 1 {
		t.Fatalf("qBittorrent received %d adds, want 1", n)
	}

	body, upload := io.Pipe()
	req, _ := http.NewRequest(http.MethodPost, s.base+"/api/v1/threads", body)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	slow := make(chan answer, 1)
	go func() {
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			slow <- answer{}
			return
		}
		defer func() { _ = resp.Body.Close() }()
		raw, _ := io.ReadAll(resp.Body)
		slow <- answer{resp.StatusCode, string(raw), resp.Header}
	}()
	if _, err := upload.Write([]byte(`{"web_search":`)); err != nil {
		t.Fatal(err)
	}
	var live string
	for deadline := time.Now().Add(30 * time.Second); live == "" && time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		_ = d.Read.QueryRow("SELECT id FROM commands WHERE pattern = '/api/v1/threads' AND state = 'running'").Scan(&live)
	}
	if live == "" {
		t.Fatalf("the slow request has no running command: %s", p.stderr.String())
	}
	time.Sleep(65 * time.Second)
	var state string
	if err := d.Read.QueryRow("SELECT state FROM commands WHERE id = ?", live).Scan(&state); err != nil || state != "running" {
		t.Fatalf("the live command after a recovery tick: %q (%v)", state, err)
	}
	if _, err := upload.Write([]byte(`false}`)); err != nil {
		t.Fatal(err)
	}
	_ = upload.Close()
	var done answer
	select {
	case done = <-slow:
	case <-time.After(60 * time.Second):
		t.Fatalf("the slow request never answered: %s", p.stderr.String())
	}
	if done.status != http.StatusCreated || done.header.Get("Atrium-Command-Id") != live {
		t.Fatalf("slow request: %d %s %v", done.status, done.body, done.header)
	}
	if data := commandStatus(t, s, token, live); data["state"] != "succeeded" || data["status"] != float64(http.StatusCreated) {
		t.Fatalf("live command after a recovery tick: %v", data)
	}
	replayed = s.do(http.MethodPost, approve, token, nil, "Idempotency-Key", "tick-1")
	if replayed.body != got.body || replayed.header.Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay after a recovery tick: %d %s", replayed.status, replayed.body)
	}
}

func TestServerFailureFreesTheKey(t *testing.T) {
	dir, _, f := moduleFixture(t, flagsExcept("", ""))
	d := asstDB(t, dir)
	id, _, err := proposals.Create(testProposalContext(), d, "retired_kind", nil, "retired", map[string]any{}, map[string]any{}, "retired", float64(time.Now().UnixNano())/1e9)
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
	approve := "/api/v1/approvals/" + strconv.FormatInt(id, 10) + "/approve"
	failed := s.do(http.MethodPost, approve, token, nil, "Idempotency-Key", "retry-1")
	command := failed.header.Get("Atrium-Command-Id")
	if failed.status < 500 || command == "" {
		t.Fatalf("approval of a retired kind: %d %s %v", failed.status, failed.body, failed.header)
	}
	if n := asstCount(t, d, "SELECT COUNT(*) FROM commands WHERE id = ? AND state = 'failed' AND idempotency_key IS NULL AND fingerprint = '' AND body IS NULL", command); n != 1 {
		t.Fatalf("the failed command still holds its key or body: %d", n)
	}
	again := s.do(http.MethodPost, approve, token, nil, "Idempotency-Key", "retry-1")
	if next := again.header.Get("Atrium-Command-Id"); again.status < 500 || next == "" || next == command || again.header.Get("Idempotent-Replayed") != "" {
		t.Fatalf("retry after a server failure: %d %s %v", again.status, again.body, again.header)
	}
	if data := commandStatus(t, s, token, command); data["state"] != "failed" || data["status"] != float64(failed.status) {
		t.Fatalf("failed command: %v", data)
	}
}

func TestKeyedResultsExpireAndCommandsArePruned(t *testing.T) {
	dir, _, f := moduleFixture(t, flagsExcept("", ""))
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), db.Key{})
	defer func() { _ = d.Close() }()
	token := localAPIToken(t, "phone")
	base, _, stop, err := startServeAppArgs(t, f.app, &lockedBuffer{}, "--auth", "local")
	if err != nil {
		t.Fatal(err)
	}
	s := &apiSite{t: t, base: base}
	body := map[string]any{"web_search": false}
	created := s.do(http.MethodPost, "/api/v1/threads", token, body, "Idempotency-Key", "age-1")
	old := created.header.Get("Atrium-Command-Id")
	if created.status != http.StatusCreated || old == "" {
		t.Fatalf("create thread: %d %s", created.status, created.body)
	}
	age := func(id string, hours int) {
		t.Helper()
		if _, err := d.Write.Exec("UPDATE commands SET created_at = created_at - ?, ended_at = ended_at - ? WHERE id = ?", hours*3600, hours*3600, id); err != nil {
			t.Fatal(err)
		}
	}
	age(old, 25)
	threads := asstCount(t, d, "SELECT COUNT(*) FROM threads")
	again := s.do(http.MethodPost, "/api/v1/threads", token, body, "Idempotency-Key", "age-1")
	fresh := again.header.Get("Atrium-Command-Id")
	if again.status != http.StatusCreated || fresh == "" || fresh == old || again.header.Get("Idempotent-Replayed") != "" {
		t.Fatalf("the key replayed after 24 hours: %d %s %v", again.status, again.body, again.header)
	}
	if n := asstCount(t, d, "SELECT COUNT(*) FROM threads"); n != threads+1 {
		t.Fatalf("%d threads after the expired key ran again, want %d", n, threads+1)
	}
	if n := asstCount(t, d, "SELECT COUNT(*) FROM commands WHERE id = ? AND idempotency_key IS NULL AND fingerprint = '' AND body IS NULL AND headers_json IS NULL AND status = 201", old); n != 1 {
		t.Fatalf("the expired command kept its key or body: %d", n)
	}
	if data := commandStatus(t, s, token, old); data["state"] != "succeeded" {
		t.Fatalf("expired command: %v", data)
	}
	age(old, 30*24)
	stop()
	base, _, stop, err = startServeAppArgs(t, f.app, &lockedBuffer{}, "--auth", "local")
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	s = &apiSite{t: t, base: base}
	expectAPI(t, s.do(http.MethodGet, "/api/v1/commands/"+old, token, nil), http.StatusNotFound, "not_found")
	if data := commandStatus(t, s, token, fresh); data["state"] != "succeeded" {
		t.Fatalf("recent command after the prune: %v", data)
	}
	replayed := s.do(http.MethodPost, "/api/v1/threads", token, body, "Idempotency-Key", "age-1")
	if replayed.header.Get("Idempotent-Replayed") != "true" || replayed.header.Get("Atrium-Command-Id") != fresh || replayed.body != again.body {
		t.Fatalf("recent key after the prune: %d %s %v", replayed.status, replayed.body, replayed.header)
	}
}

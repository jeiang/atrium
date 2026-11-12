package main

import (
	"context"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/sdnotify/sdnotifytest"
)

func TestServeDrainSettlesAHeldExternalWriteThenStopsCleanly(t *testing.T) {
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	var posts atomic.Int32
	arrived := make(chan struct{}, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseAll := func() { releaseOnce.Do(func() { close(release) }) }
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost && r.URL.Path == "/public/v1/trackers" {
			posts.Add(1)
			arrived <- struct{}{}
			select {
			case <-r.Context().Done():
				return
			case <-release:
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"data":{"tracker":{"trackerId":"trk_1","trackingNumber":"1Z999AA10123456781"}}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"trackings":[]}}`))
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(releaseAll)
	setEnv(t, "ATRIUM_HOST_CONFIG", ship24Host(t, dir, srv.URL+"/public/v1", "good"))
	if ran := runApp(t, "agent", "run", "--text", "warm"); ran.code != cli.ExitOK {
		t.Fatalf("warm run: %d %s", ran.code, ran.stderr)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
	defer func() { _ = d.Close() }()
	held := trackingProposal(t, api, d, 1)
	asstExec(t, d, "UPDATE jobs SET next_due = NULL")

	opsBase := opsAddr(t)
	notify := sdnotifytest.Listen(t, 200*time.Millisecond)
	for name, value := range notify.Env() {
		setEnv(t, name, value)
	}
	s := serveCLIProcess(t, dir)
	notify.Wait("READY=1")
	base := "http://" + s.addr
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if status, _, _ := fetch(t, client, http.MethodGet, base+"/chat", nil); status != http.StatusOK {
		t.Fatalf("GET /chat = %d", status)
	}
	answered := make(chan int, 1)
	go func() {
		resp, err := approveRequest(context.Background(), client, base, held)
		if err != nil {
			answered <- 0
			return
		}
		_ = resp.Body.Close()
		answered <- resp.StatusCode
	}()
	<-arrived

	if err := s.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	notify.Wait("STOPPING=1")
	status, body, _ := fetch(t, http.DefaultClient, http.MethodGet, opsBase+"/readyz", nil)
	if status != http.StatusServiceUnavailable || !strings.Contains(body, "draining") {
		t.Fatalf("readyz while draining = %d %s, want 503 naming the drain", status, body)
	}
	if status, body, _ := fetch(t, http.DefaultClient, http.MethodGet, opsBase+"/livez", nil); status != http.StatusOK {
		t.Fatalf("livez while draining = %d %s, want 200", status, body)
	}
	if status, body, _ := fetch(t, http.DefaultClient, http.MethodGet, opsBase+"/healthz", nil); status != http.StatusOK || !strings.Contains(body, `"draining":true`) || !strings.Contains(body, `"name":"scheduler"`) {
		t.Fatalf("healthz while draining = %d %s, want draining with the scheduler listed", status, body)
	}
	if state, _ := trackingState(t, d, held); state != string(ledger.Intent) {
		t.Fatalf("the held write is %q while serve drains, want intent", state)
	}
	if err := s.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Second)
	select {
	case err := <-s.done:
		t.Fatalf("serve exited before the held write was released: %v %s", err, s.stderr.String())
	default:
	}
	releaseAll()
	select {
	case err := <-s.done:
		s.err, s.waited = err, true
	case <-time.After(30 * time.Second):
		t.Fatalf("serve did not stop within 30s of the release: %s", s.stderr.String())
	}
	if code := exitCode(s.err); code != cli.ExitOK {
		t.Fatalf("serve exit %d after a settled drain: %s", code, s.stderr.String())
	}
	if code := <-answered; code != http.StatusOK {
		t.Fatalf("the approval held across the drain answered %d, want 200", code)
	}
	if state, settled := trackingState(t, d, held); !settled || state != string(ledger.Done) {
		t.Fatalf("the write held across the drain is %q (settled %v), want done", state, settled)
	}
	if n := posts.Load(); n != 1 {
		t.Fatalf("the carrier saw %d writes, want one", n)
	}
	var proposal string
	if err := d.Read.QueryRow("SELECT status FROM proposals WHERE id = ?", held).Scan(&proposal); err != nil || proposal != "applied" {
		t.Fatalf("the held proposal is %q (%v), want applied", proposal, err)
	}
	for _, bad := range []string{"database is closed", "serve did not stop cleanly"} {
		if strings.Contains(s.stderr.String(), bad) {
			t.Fatalf("the drain logged %q: %s", bad, s.stderr.String())
		}
	}
	messages := notify.Messages()
	for _, once := range []string{"READY=1", "STOPPING=1"} {
		if n := countMessage(messages, once); n != 1 {
			t.Fatalf("serve sent %s %d times, want once: %q", once, n, messages)
		}
	}

	idle := restartServe(t, dir)
	started := time.Now()
	if code := idle.stop(t, syscall.SIGTERM); code != cli.ExitOK {
		t.Fatalf("idle serve exit %d: %s", code, idle.stderr.String())
	}
	if took := time.Since(started); took > 10*time.Second {
		t.Fatalf("idle serve took %s to stop, want a quick stop", took)
	}
	if n := posts.Load(); n != 1 {
		t.Fatalf("the carrier saw %d writes after a restart, want one", n)
	}
}

func countMessage(messages []string, want string) int {
	return len(slices.DeleteFunc(slices.Clone(messages), func(m string) bool { return !strings.Contains(m, want) }))
}

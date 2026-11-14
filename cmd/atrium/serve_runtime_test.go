package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/proposals"
)

var serveRuntimeServices = []string{"recovery", "embedding-index", "connection-health", "notifications-monitor", "assistant", "scheduler"}

func serviceStarts(logs *lockedBuffer, name string) int {
	return strings.Count(logs.String(), "service "+name+" started")
}

func serviceFailures(logs *lockedBuffer, name string) int {
	return strings.Count(logs.String(), "service "+name+" failed; restarting in ")
}

func titledNotices(t *testing.T, d *db.DB, prefix string) int {
	t.Helper()
	return asstCount(t, d, "SELECT COUNT(*) FROM notifications WHERE title LIKE ?", prefix+"%")
}

func exitWithin(t *testing.T, cmd *exec.Cmd, limit time.Duration) int {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return exitCode(err)
	case <-time.After(limit):
		_ = cmd.Process.Kill()
		<-done
		t.Fatalf("%v did not exit within %s", cmd.Args, limit)
		return -1
	}
}

type faultyEmbedder struct {
	*httptest.Server
	broken atomic.Bool
	resets atomic.Int64
}

func newFaultyEmbedder(t *testing.T) *faultyEmbedder {
	healthy := newFakeOpenAI(t)
	f := &faultyEmbedder{}
	f.broken.Store(true)
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !f.broken.Load() {
			healthy.Config.Handler.ServeHTTP(w, r)
			return
		}
		f.resets.Add(1)
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		if tcp, ok := conn.(*net.TCPConn); ok {
			_ = tcp.SetLinger(0)
		}
		_ = conn.Close()
	}))
	t.Cleanup(f.Close)
	return f
}

func TestServeRuntimeRunsADueJobOnceBesideAConcurrentTick(t *testing.T) {
	d, api, f, before := startWeeklyReview(t, asstReply("reviewed"))
	release := asstHold(t, api)
	_, logs := startServeLogged(t, f.dir)
	asstWaitCount(t, d, 1, weeklyRunning)
	waitUntil(t, 30*time.Second, "the weekly review request held inside serve", func() bool { return len(api.Requests()) > before })

	tick, stderr := tickProcess(t)
	if code := exitWithin(t, tick, 60*time.Second); code != cli.ExitOK && code != cli.ExitTempFail {
		t.Fatalf("concurrent agent tick exit %d: %s", code, stderr)
	}
	if n := len(api.Requests()) - before; n != 1 {
		t.Fatalf("%d weekly review requests while serve holds the run, want 1", n)
	}
	if n := schedulerRuns(t, d, "weekly-review"); n != 1 {
		t.Fatalf("%d weekly review runs while serve holds the run, want 1", n)
	}

	release()
	waitUntil(t, 60*time.Second, "the weekly review to settle", schedulerTerminal(d, "weekly-review"))
	if status, text := schedulerStatus(t, d, "weekly-review"); status != "ok" {
		t.Fatalf("weekly review status %s: %s", status, text)
	}
	after, afterErr := tickProcess(t)
	if code := exitWithin(t, after, 60*time.Second); code != cli.ExitOK {
		t.Fatalf("agent tick after the run exit %d: %s", code, afterErr)
	}
	if n := len(api.Requests()) - before; n != 1 {
		t.Errorf("%d weekly review requests after release, want 1", n)
	}
	if n := schedulerRuns(t, d, "weekly-review"); n != 1 {
		t.Errorf("%d weekly review runs after release, want 1", n)
	}
	if n := serviceStarts(logs, "scheduler"); n != 1 {
		t.Errorf("service scheduler started %d times, want 1", n)
	}
}

func TestServeRuntimeStartsEachMonitorOnce(t *testing.T) {
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	embedder := newFakeOpenAI(t)
	setEnv(t, "ATRIUM_HOST_CONFIG", embeddingHost(t, dir, embedder.URL+"/v1", "embeddinggemma", ""))
	if got := runApp(t, "connections", "test", "service/embedding"); got.code != cli.ExitOK {
		t.Fatalf("connections test service/embedding: exit %d stdout %q stderr %q", got.code, got.stdout, got.stderr)
	}
	d := asstDB(t, dir)
	_, _, stop := startServeStoppable(t, dir)
	asstWaitCount(t, d, 1, "SELECT COUNT(*) FROM embed_index WHERE state = 'active' AND url != ''")
	stop()

	if got := runApp(t, "backup", filepath.Join(dir, "missing", "x.backup")); got.code == cli.ExitOK {
		t.Fatal("a backup into a missing directory succeeded")
	}
	embedder.setStatus(http.StatusInternalServerError)
	embedNotices := titledNotices(t, d, "atrium: Embedding ")
	if n := titledNotices(t, d, "atrium: backup is stale"); n != 0 {
		t.Fatalf("%d stale backup notices before serve, want 0", n)
	}

	_, logs := startServeLogged(t, dir)
	waitUntil(t, 30*time.Second, "the stale backup notice", func() bool { return titledNotices(t, d, "atrium: backup is stale") > 0 })
	waitUntil(t, 30*time.Second, "the embedding connection to fail", func() bool { return connectionState(t, dir, "service/embedding") == "failing" })
	waitUntil(t, 30*time.Second, "every service to start", func() bool {
		for _, name := range serveRuntimeServices {
			if serviceStarts(logs, name) == 0 {
				return false
			}
		}
		return true
	})
	time.Sleep(3 * time.Second)

	if n := titledNotices(t, d, "atrium: backup is stale"); n != 1 {
		t.Errorf("%d stale backup notices, want 1", n)
	}
	if n := asstCount(t, d, "SELECT COUNT(*) FROM pass_run_alerts WHERE job = 'backup'"); n != 1 {
		t.Errorf("%d stale backup alerts, want 1", n)
	}
	if n := titledNotices(t, d, "atrium: Embedding ") - embedNotices; n != 1 {
		t.Errorf("%d embedding transition notices, want 1", n)
	}
	for _, name := range serveRuntimeServices {
		if n := serviceStarts(logs, name); n != 1 {
			t.Errorf("service %s started %d times, want 1", name, n)
		}
		if n := serviceFailures(logs, name); n != 0 {
			t.Errorf("service %s failed %d times, want 0", name, n)
		}
	}
}

func TestServeRuntimeEmbedderFaultRestartsOnlyItsService(t *testing.T) {
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	embedder := newFaultyEmbedder(t)
	setEnv(t, "ATRIUM_HOST_CONFIG", embeddingHost(t, dir, embedder.URL+"/v1", "embeddinggemma", ""))
	opsBase := opsAddr(t)
	d := asstDB(t, dir)
	base, logs := startServeLogged(t, dir)

	waitUntil(t, 60*time.Second, "the embedding index service to restart after a reset", func() bool {
		return serviceStarts(logs, "embedding-index") >= 2 && serviceFailures(logs, "embedding-index") >= 1
	})
	if embedder.resets.Load() == 0 {
		t.Fatal("the embedder never saw a request")
	}
	if status, state := readiness(t, opsBase); status != http.StatusOK {
		t.Errorf("readyz during the embedder fault = %d %s, want 200", status, state)
	}
	waitUntil(t, 60*time.Second, "healthz to report the embedder outage as degraded", func() bool {
		status, body, _ := fetch(t, http.DefaultClient, http.MethodGet, opsBase+"/healthz", nil)
		return status == http.StatusOK && strings.Contains(body, `"degraded":true`) && strings.Contains(body, `"ready":true`) && strings.Contains(body, `"draining":false`)
	})
	if status, body, _ := fetch(t, http.DefaultClient, http.MethodGet, opsBase+"/livez", nil); status != http.StatusOK {
		t.Errorf("livez during the embedder fault = %d %s, want 200", status, body)
	}
	for _, page := range []struct{ path, want string }{
		{"/healthz", ""},
		{"/scheduled", "Every day at 02:30"},
		{"/settings/notifications", "Channels"},
	} {
		if status, body, _ := fetch(t, http.DefaultClient, http.MethodGet, base+page.path, nil); status != http.StatusOK || !strings.Contains(body, page.want) {
			t.Errorf("GET %s during the embedder fault = %d: %.300s", page.path, status, body)
		}
	}
	for _, name := range []string{"recovery", "connection-health", "notifications-monitor", "assistant", "scheduler"} {
		if n := serviceStarts(logs, name); n != 1 {
			t.Errorf("service %s started %d times during the embedder fault, want 1", name, n)
		}
	}

	embedder.broken.Store(false)
	asstWaitCount(t, d, 1, "SELECT COUNT(*) FROM embed_index WHERE state = 'active' AND url != ''")
	starts, failures := serviceStarts(logs, "embedding-index"), serviceFailures(logs, "embedding-index")
	time.Sleep(3 * time.Second)
	if n := serviceStarts(logs, "embedding-index"); n != starts {
		t.Errorf("service embedding-index started %d times after healing, was %d", n, starts)
	}
	if n := serviceFailures(logs, "embedding-index"); n != failures {
		t.Errorf("service embedding-index failed %d times after healing, was %d", n, failures)
	}
	if status, _, _ := fetch(t, http.DefaultClient, http.MethodGet, base+"/scheduled", nil); status != http.StatusOK {
		t.Errorf("GET /scheduled after healing = %d", status)
	}
}

func TestServeRuntimeDisablingAModuleStopsItsJobAndRestartsOneInstance(t *testing.T) {
	d, api, f, before := startWeeklyReview(t, asstReply("reviewed"))
	release := asstHold(t, api)
	_, logs := startServeLogged(t, f.dir)
	asstWaitCount(t, d, 1, weeklyRunning)
	waitUntil(t, 30*time.Second, "the weekly review request held inside serve", func() bool { return len(api.Requests()) > before })
	waitUntil(t, 30*time.Second, "the assistant service", func() bool { return serviceStarts(logs, "assistant") == 1 })

	asstSet(t, module.FlagKey(module.Assistant), "false")
	waitUntil(t, 30*time.Second, "the held weekly review to settle", schedulerTerminal(d, "weekly-review"))
	status, text := schedulerStatus(t, d, "weekly-review")
	if status != "skipped_module" || !strings.Contains(text, "the assistant module was disabled; start the action again") {
		t.Fatalf("weekly review after disabling the assistant = %s %q, want skipped_module", status, text)
	}
	release()
	time.Sleep(time.Second)
	if n := len(api.Requests()) - before; n != 1 {
		t.Errorf("%d weekly review requests after disabling, want 1", n)
	}
	if n := serviceStarts(logs, "assistant"); n != 1 {
		t.Errorf("service assistant started %d times while disabled, want 1", n)
	}

	asstSet(t, module.FlagKey(module.Assistant), "true")
	waitUntil(t, 30*time.Second, "the re-enabled assistant service", func() bool { return serviceStarts(logs, "assistant") >= 2 })
	time.Sleep(2 * time.Second)
	if n := serviceStarts(logs, "assistant"); n != 2 {
		t.Errorf("service assistant started %d times after re-enabling, want 2", n)
	}
	if n := serviceStarts(logs, "scheduler"); n != 1 {
		t.Errorf("service scheduler started %d times, want 1", n)
	}
	if n := len(api.Requests()) - before; n != 1 {
		t.Errorf("%d weekly review requests after re-enabling, want 1", n)
	}
	if n := schedulerRuns(t, d, "weekly-review"); n != 1 {
		t.Errorf("%d weekly review runs after re-enabling, want 1", n)
	}
}

func TestServeRuntimeRecoversBeforeTheFirstRequest(t *testing.T) {
	t.Parallel()
	dir, _, f := moduleFixture(t, flagsExcept("", ""))
	d := asstDB(t, dir)
	now := float64(time.Now().UnixNano()) / 1e9
	stuck, _, err := proposals.Create(testProposalContext(), d, "rebalance_split", nil, "stuck", map[string]any{}, map[string]any{}, "stuck", now-5000)
	if err != nil {
		t.Fatal(err)
	}
	asstExec(t, d, "UPDATE proposals SET status = 'approved', decided_ts = ? WHERE id = ?", now-4000, stuck)

	addrFile := filepath.Join(t.TempDir(), "serve-addr")
	setEnv(t, "ATRIUM_SERVE_ADDR_FILE", addrFile)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	logs := &lockedBuffer{}
	go func() { done <- f.app.Run(ctx, []string{"serve", "--port", "0", "--auth", "local"}, io.Discard, logs) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			t.Error("serve did not stop on cancel")
		}
	})
	var base string
	waitUntil(t, 60*time.Second, "the serve address", func() bool {
		raw, err := os.ReadFile(addrFile)
		if err != nil {
			return false
		}
		addr := strings.TrimSpace(string(raw))
		if _, _, err := net.SplitHostPort(addr); err != nil {
			return false
		}
		base = "http://" + addr
		return true
	})

	card := base + "/proposals/" + strconv.FormatInt(stuck, 10) + "/card"
	var status int
	var body string
	waitUntil(t, 60*time.Second, "the first response", func() bool {
		resp, err := http.Get(card)
		if err != nil {
			return false
		}
		defer func() { _ = resp.Body.Close() }()
		raw, _ := io.ReadAll(resp.Body)
		status, body = resp.StatusCode, string(raw)
		return true
	})
	if status != http.StatusOK || !strings.Contains(body, `data-status="failed"`) {
		t.Fatalf("first response for the stuck proposal = %d: %.500s\n%s", status, body, logs.String())
	}
	if n := asstCount(t, d, "SELECT COUNT(*) FROM proposals WHERE id = ? AND status = 'failed' AND recovered = 1", stuck); n != 1 {
		t.Errorf("stuck proposal not recovered: %d rows", n)
	}
}

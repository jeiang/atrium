package web

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/mail/drafts"
	"github.com/jeiang/atrium/internal/proposals"
	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/sdnotify/sdnotifytest"
)

func runServe(t *testing.T, ctx context.Context, args ...string) (int, string, string) {
	t.Helper()
	t.Setenv("COLUMNS", "")
	app := cli.New("atrium", nil)
	app.Add(ServeCommand())
	var stdout, stderr bytes.Buffer
	code := app.Run(ctx, append([]string{"serve"}, args...), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestServeProxyNeedsAllowedHost(t *testing.T) {
	t.Setenv("ATRIUM_PUBLIC_HOST", "  ")
	code, _, stderr := runServe(t, context.Background(), "--port", "1")
	if code != cli.ExitError || !strings.Contains(stderr, "proxy mode needs --allowed-host or ATRIUM_PUBLIC_HOST") {
		t.Errorf("code %d stderr %q", code, stderr)
	}
}

func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	_, port, _ := net.SplitHostPort(l.Addr().String())
	return port
}

func TestServeRecoversStuckDraftsAndStopsOnCancel(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ATRIUM_DB", filepath.Join(dir, "atrium.db"))
	t.Setenv("ATRIUM_CACHE_DIR", filepath.Join(dir, "cache"))
	t.Setenv("ATRIUM_PUBLIC_HOST", "")
	notify := sdnotifytest.Listen(t, 200*time.Millisecond)
	ctx := context.Background()
	seed, err := db.Connect(filepath.Join(dir, "atrium.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, seed, filepath.Join(dir, "atrium.db")); err != nil {
		t.Fatal(err)
	}
	if _, err := seed.Write.Exec("INSERT INTO drafts (thread_id, account, to_addrs, subject, body, status, created_at, updated_at) VALUES (NULL, 'gmail', 'a@x.test', 's', 'b', 'sending', 1000, 1000)"); err != nil {
		t.Fatal(err)
	}
	_ = seed.Close()

	cancelCtx, cancel := context.WithCancel(ctx)
	port := freePort(t)
	done := make(chan int, 1)
	go func() {
		code, _, _ := runServe(t, cancelCtx, "--port", port, "--auth", "local")
		done <- code
	}()
	url := "http://127.0.0.1:" + port
	var resp *http.Response
	for range 100 {
		if resp, err = http.Get(url + "/healthz"); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("healthz = %d", resp.StatusCode)
	}
	notify.Wait("WATCHDOG=1")
	notify.Wait("READY=1")
	notify.Wait("STATUS=serving 127.0.0.1:" + port)
	cancel()
	notify.Wait("STOPPING=1")
	select {
	case code := <-done:
		if code != cli.ExitOK {
			t.Errorf("exit code %d", code)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("serve did not stop on cancel")
	}
	after, err := db.Connect(filepath.Join(dir, "atrium.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = after.Close() }()
	var status string
	if err := after.Read.QueryRow("SELECT status FROM drafts").Scan(&status); err != nil || status != drafts.Unknown {
		t.Errorf("stuck draft status = %q, %v", status, err)
	}
}

func TestServeRecoversAStuckApprovedProposalOnlyOnceItIsOlderThanTheBound(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ATRIUM_DB", filepath.Join(dir, "atrium.db"))
	t.Setenv("ATRIUM_CACHE_DIR", filepath.Join(dir, "cache"))
	t.Setenv("ATRIUM_PUBLIC_HOST", "")
	ctx := context.Background()
	const base = 1_792_080_000.0
	var advance atomic.Int64
	oldInterval, oldClock := recoverInterval, recoverClock
	recoverInterval = 50 * time.Millisecond
	recoverClock = func() float64 { return base + float64(advance.Load()) }
	t.Cleanup(func() { recoverInterval, recoverClock = oldInterval, oldClock })
	seed, err := db.Connect(filepath.Join(dir, "atrium.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, seed, filepath.Join(dir, "atrium.db")); err != nil {
		t.Fatal(err)
	}
	pid, _, err := proposals.Create(ctx, seed, "rebalance_split", nil, "title", py.Dict{}, py.Dict{}, "fp", base)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := seed.Write.Exec("UPDATE proposals SET status = 'approved' WHERE id = ?", pid); err != nil {
		t.Fatal(err)
	}
	if _, err := proposals.BeginAction(ctx, seed.Write, pid, py.Dict{{Key: "op", Value: "move"}}, base); err != nil {
		t.Fatal(err)
	}
	_ = seed.Close()
	status := func() string {
		check, err := db.Connect(filepath.Join(dir, "atrium.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = check.Close() }()
		var s string
		if err := check.Read.QueryRow("SELECT status FROM proposals WHERE id = ?", pid).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}

	cancelCtx, cancel := context.WithCancel(ctx)
	port := freePort(t)
	done := make(chan int, 1)
	go func() {
		code, _, _ := runServe(t, cancelCtx, "--port", port, "--auth", "local")
		done <- code
	}()
	var resp *http.Response
	for range 100 {
		if resp, err = http.Get("http://127.0.0.1:" + port + "/healthz"); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	time.Sleep(300 * time.Millisecond)
	if s := status(); s != proposals.Approved {
		t.Fatalf("status inside the bound = %q, want approved", s)
	}
	advance.Store(proposals.StuckSeconds + 1)
	for range 100 {
		if status() == proposals.Failed {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if s := status(); s != proposals.Failed {
		t.Fatalf("status past the bound = %q, want failed", s)
	}
	cancel()
	select {
	case code := <-done:
		if code != cli.ExitOK {
			t.Errorf("exit code %d", code)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("serve did not stop on cancel")
	}
}

func TestServeRecoversADraftThatGoesStaleWhileRunning(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "atrium.db")
	t.Setenv("ATRIUM_DB", dbPath)
	t.Setenv("ATRIUM_CACHE_DIR", filepath.Join(dir, "cache"))
	t.Setenv("ATRIUM_PUBLIC_HOST", "")
	oldInterval := recoverInterval
	recoverInterval = 50 * time.Millisecond
	t.Cleanup(func() { recoverInterval = oldInterval })
	ctx := context.Background()
	d, err := db.Connect(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	if err := db.Migrate(ctx, d, dbPath); err != nil {
		t.Fatal(err)
	}
	fresh := float64(time.Now().Unix()) - drafts.SendingStaleSeconds + 2
	if _, err := d.Write.Exec("INSERT INTO drafts (thread_id, account, to_addrs, subject, body, status, created_at, updated_at) VALUES (NULL, 'gmail', 'a@x.test', 's', 'b', 'sending', ?, ?)", fresh, fresh); err != nil {
		t.Fatal(err)
	}
	cancelCtx, cancel := context.WithCancel(ctx)
	port := freePort(t)
	done := make(chan struct{})
	go func() {
		runServe(t, cancelCtx, "--port", port, "--auth", "local")
		close(done)
	}()
	status := ""
	for range 200 {
		if err := d.Read.QueryRow("SELECT status FROM drafts").Scan(&status); err == nil && status == drafts.Unknown {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	<-done
	if status != drafts.Unknown {
		t.Errorf("draft status = %q, want %q", status, drafts.Unknown)
	}
}

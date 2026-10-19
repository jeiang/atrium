package web

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/mail/drafts"
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

func TestServePortIsRequired(t *testing.T) {
	code, _, stderr := runServe(t, context.Background())
	want := "usage: atrium serve [-h] [--host HOST] --port PORT [--auth {proxy,local}]\n" +
		"                    [--allowed-host ALLOWED_HOST]\n" +
		"atrium serve: error: the following arguments are required: --port\n"
	if code != cli.ExitUsage || stderr != want {
		t.Errorf("code %d stderr %q", code, stderr)
	}
}

func TestServeRejectsUnknownAuthMode(t *testing.T) {
	code, _, stderr := runServe(t, context.Background(), "--port", "1", "--auth", "open")
	if code != cli.ExitUsage || !strings.HasSuffix(stderr, "\natrium serve: error: argument --auth: invalid choice: 'open' (choose from 'proxy', 'local')\n") {
		t.Errorf("code %d stderr %q", code, stderr)
	}
}

func TestServeProxyNeedsAllowedHost(t *testing.T) {
	t.Setenv("ATRIUM_PUBLIC_HOST", "  ")
	code, _, stderr := runServe(t, context.Background(), "--port", "1")
	if code != cli.ExitError || !strings.Contains(stderr, "proxy mode needs --allowed-host or ATRIUM_PUBLIC_HOST") {
		t.Errorf("code %d stderr %q", code, stderr)
	}
}

func TestPublicHosts(t *testing.T) {
	env := config.Map(map[string]string{"ATRIUM_PUBLIC_HOST": " atrium.test "})
	if got := publicHosts([]string{"a", "b"}, env); len(got) != 2 || got[0] != "a" {
		t.Errorf("flags lost: %v", got)
	}
	if got := publicHosts(nil, env); len(got) != 1 || got[0] != "atrium.test" {
		t.Errorf("env fallback: %v", got)
	}
	if got := publicHosts(nil, config.Map(nil)); got != nil {
		t.Errorf("unset: %v", got)
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
	cancel()
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

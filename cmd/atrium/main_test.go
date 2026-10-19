package main

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/cli"
)

func TestNoSubcommandIsUsageError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := newApp().Run(context.Background(), nil, &stdout, &stderr); code != cli.ExitUsage {
		t.Fatalf("exit code = %d, want %d", code, cli.ExitUsage)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want empty", stdout.String())
	}
}

func TestHelpExitsZero(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := newApp().Run(context.Background(), []string{"--help"}, &stdout, &stderr); code != cli.ExitOK {
		t.Fatalf("exit code = %d, want %d", code, cli.ExitOK)
	}
	if !bytes.HasPrefix(stdout.Bytes(), []byte("usage: atrium [-h]")) {
		t.Errorf("stdout = %q, want usage line", stdout.String())
	}
}

func TestServeRendersNotificationsPage(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ATRIUM_DB", filepath.Join(dir, "atrium.db"))
	t.Setenv("ATRIUM_CACHE_DIR", filepath.Join(dir, "cache"))
	t.Setenv("ATRIUM_PUBLIC_HOST", "")
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(l.Addr().String())
	_ = l.Close()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	go func() {
		var stdout, stderr bytes.Buffer
		done <- newApp().Run(ctx, []string{"serve", "--port", port, "--auth", "local"}, &stdout, &stderr)
	}()
	var resp *http.Response
	for range 100 {
		if resp, err = http.Get("http://127.0.0.1:" + port + "/settings/notifications"); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "No notifications") {
		t.Errorf("GET /settings/notifications = %d: %s", resp.StatusCode, body)
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

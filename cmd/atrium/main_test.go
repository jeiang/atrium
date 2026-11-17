package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/logx"
)

func startServe(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	setEnv(t, "ATRIUM_DB", filepath.Join(dir, "atrium.db"))
	setEnv(t, "ATRIUM_CACHE_DIR", filepath.Join(dir, "cache"))
	setEnv(t, "ATRIUM_PUBLIC_HOST", "")
	seedFlagsAt(t, newTestApp(t), filepath.Join(dir, "atrium.db"), allFlagsTrue())
	return startServeIn(t, dir)
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func startServeIn(t *testing.T, dir string) string {
	t.Helper()
	base, _, _ := startServeStoppable(t, dir)
	return base
}

func startServeLogged(t *testing.T, dir string) (string, *lockedBuffer) {
	t.Helper()
	base, logs, _ := startServeWith(t, dir, true)
	return base, logs
}

func startServeStoppable(t *testing.T, dir string) (string, *lockedBuffer, func()) {
	t.Helper()
	return startServeWith(t, dir, false)
}

func startServeWith(t *testing.T, dir string, logged bool) (string, *lockedBuffer, func()) {
	t.Helper()
	base, logs, stop, err := startServeOnce(t, dir, logged)
	if err != nil {
		t.Fatalf("serve did not start: %v", err)
	}
	t.Cleanup(stop)
	return base, logs, stop
}

func startServeOnce(t *testing.T, dir string, logged bool) (string, *lockedBuffer, func(), error) {
	t.Helper()
	setEnv(t, "ATRIUM_DB", filepath.Join(dir, "atrium.db"))
	setEnv(t, "ATRIUM_CACHE_DIR", filepath.Join(dir, "cache"))
	setEnv(t, "ATRIUM_PUBLIC_HOST", "")
	app := newTestApp(t)
	if f := fixtureFor(t); f != nil {
		app = f.app
	}
	logs := &lockedBuffer{}
	if logged {
		logx.Setup(logs)
	}
	return startServeAppOnce(t, app, logs)
}

func startServeApp(t *testing.T, app *cli.App, logs *lockedBuffer) (string, *lockedBuffer, func()) {
	t.Helper()
	base, logs, stop, err := startServeAppOnce(t, app, logs)
	if err != nil {
		t.Fatalf("serve did not start: %v", err)
	}
	t.Cleanup(stop)
	return base, logs, stop
}

func startServeAppOnce(t *testing.T, app *cli.App, logs *lockedBuffer) (string, *lockedBuffer, func(), error) {
	t.Helper()
	return startServeAppArgs(t, app, logs, "--auth", "local")
}

func startServeAppArgs(t *testing.T, app *cli.App, logs *lockedBuffer, auth ...string) (string, *lockedBuffer, func(), error) {
	t.Helper()
	addrFile := filepath.Join(t.TempDir(), "serve-addr")
	setEnv(t, "ATRIUM_SERVE_ADDR_FILE", addrFile)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	var exitCode atomic.Int32
	exitCode.Store(-1)
	go func() {
		var stdout bytes.Buffer
		code := app.Run(ctx, append([]string{"serve", "--port", "0"}, auth...), &stdout, logs)
		exitCode.Store(int32(code))
		done <- code
	}()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			select {
			case code := <-done:
				if code != cli.ExitOK && !t.Failed() {
					t.Errorf("exit code %d", code)
				}
			case <-time.After(15 * time.Second):
				t.Error("serve did not stop on cancel")
			}
		})
	}
	base, err := waitForServeAddr(t, addrFile, logs, &exitCode, cancel)
	if err != nil {
		cancel()
		<-done
		return "", logs, nil, err
	}
	for deadline := time.Now().Add(60 * time.Second); time.Now().Before(deadline); {
		if resp, err := http.Get(base + "/healthz"); err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return base, logs, stop, nil
			}
		}
		if code := exitCode.Load(); code >= 0 {
			cancel()
			<-done
			return "", logs, nil, fmt.Errorf("serve exited with %d: %s", code, logs.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	<-done
	return "", logs, nil, fmt.Errorf("serve did not start: %s", logs.String())
}

func waitForServeAddr(t *testing.T, path string, logs *lockedBuffer, exitCode *atomic.Int32, cancel context.CancelFunc) (string, error) {
	t.Helper()
	for deadline := time.Now().Add(60 * time.Second); time.Now().Before(deadline); {
		if raw, err := os.ReadFile(path); err == nil {
			addr := strings.TrimSpace(string(raw))
			if _, _, err := net.SplitHostPort(addr); err == nil {
				return "http://" + addr, nil
			}
		}
		if code := exitCode.Load(); code >= 0 {
			return "", fmt.Errorf("serve exited with %d: %s", code, logs.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	return "", fmt.Errorf("serve did not report its address: %s", logs.String())
}

func fetch(t *testing.T, client *http.Client, method, url string, form url.Values) (int, string, http.Header) {
	t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatal(err)
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		for _, c := range client.Jar.Cookies(req.URL) {
			if c.Name == "atrium_csrf" {
				req.Header.Set("X-CSRF-Token", c.Value)
			}
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	text, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(text), resp.Header
}

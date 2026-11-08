package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
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
	return startServeIn(t, t.TempDir())
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
	app := newTestApp(t, nil)
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
	addrFile := filepath.Join(t.TempDir(), "serve-addr")
	setEnv(t, "ATRIUM_SERVE_ADDR_FILE", addrFile)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	var exitCode atomic.Int32
	exitCode.Store(-1)
	go func() {
		var stdout bytes.Buffer
		code := app.Run(ctx, []string{"serve", "--port", "0", "--auth", "local"}, &stdout, logs)
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

func TestServeRendersMailPages(t *testing.T) {
	t.Parallel()
	base := startServe(t)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for _, tc := range []struct {
		path   string
		status int
		want   string
	}{
		{"/triage", 200, "No decisions match"},
		{"/triage?stage=llm", 200, "No decisions match"},
		{"/triage?stage=nonsense", 422, "Filter not understood"},
		{"/rules", 200, "Nothing learned yet."},
		{"/rules/new", 200, "Generated if empty"},
		{"/rules/condition", 200, "Remove condition"},
		{"/rules/missing/edit", 404, "Nothing here"},
		{"/messages/1", 404, "Nothing here"},
		{"/messages/1/correct", 404, "Nothing here"},
	} {
		if status, body, _ := fetch(t, client, "GET", base+tc.path, nil); status != tc.status || !strings.Contains(body, tc.want) {
			t.Errorf("GET %s = %d, want %d with %q: %.300s", tc.path, status, tc.status, tc.want, body)
		}
	}
	created := url.Values{"id": {"shop"}, "action": {"file"}, "dest": {"Bills"}, "field": {"from_addr"}, "op": {"eq"}, "value": {"a@shop.test"}}
	status, _, header := fetch(t, client, "POST", base+"/rules", created)
	if status != http.StatusSeeOther || header.Get("Location") != "/rules?saved=shop&created=1" {
		t.Fatalf("POST /rules = %d %q", status, header.Get("Location"))
	}
	if status, body, _ := fetch(t, client, "GET", base+header.Get("Location"), nil); status != http.StatusOK || !strings.Contains(body, "Rule created") || !strings.Contains(body, `id="rule-shop"`) {
		t.Errorf("saved rules page = %d: %.300s", status, body)
	}
	if status, _, _ := fetch(t, client, "POST", base+"/rules", nil); status != http.StatusForbidden {
		t.Errorf("POST /rules without a form or token = %d", status)
	}
	if status, _, header := fetch(t, client, "POST", base+"/live/pause", url.Values{}); status != http.StatusSeeOther || header.Get("Location") != "/triage" {
		t.Errorf("POST /live/pause = %d %q", status, header.Get("Location"))
	}
}

func TestServeRendersReviewPages(t *testing.T) {
	t.Parallel()
	base := startServe(t)
	for _, path := range []string{"/review", "/proposals", "/activity", "/drafts", "/loops", "/watches"} {
		if status, _, _ := fetch(t, http.DefaultClient, "GET", base+path, nil); status != http.StatusOK {
			t.Errorf("GET %s = %d", path, status)
		}
	}
}

func TestServeRendersNotionPages(t *testing.T) {
	t.Parallel()
	base := startServe(t)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if status, body, _ := fetch(t, client, "GET", base+"/notion/missing", nil); status != http.StatusNotFound || !strings.Contains(body, "Nothing here") {
		t.Errorf("GET /notion/missing = %d: %.300s", status, body)
	}
	for _, tc := range []struct {
		path   string
		form   url.Values
		status int
		want   string
	}{
		{"/notion/missing/link", url.Values{}, http.StatusUnprocessableEntity, `"target"`},
		{"/notion/missing/link", url.Values{"target": {"other"}}, http.StatusOK, "unknown page missing"},
		{"/notion/missing/edit", url.Values{"instruction": {"shorten"}}, http.StatusOK, "unknown page missing"},
		{"/projects/abc/wiki-hub", url.Values{}, http.StatusUnprocessableEntity, `"project_id"`},
		{"/projects/1/wiki-hub", url.Values{}, http.StatusNotFound, "Nothing here"},
	} {
		if status, body, _ := fetch(t, client, "POST", base+tc.path, tc.form); status != tc.status || !strings.Contains(body, tc.want) {
			t.Errorf("POST %s = %d, want %d with %q: %.300s", tc.path, status, tc.status, tc.want, body)
		}
	}
}

func TestServeRendersAssistantPages(t *testing.T) {
	t.Parallel()
	base := startServe(t)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for _, tc := range []struct {
		path   string
		status int
		want   string
	}{
		{"/projects/new", 200, "New project"},
		{"/projects/1", 404, "Nothing here"},
		{"/projects/1/edit", 404, "Nothing here"},
		{"/scheduled", 200, "Every day at 02:30"},
		{"/library/files", 200, "No files yet"},
		{"/library/wiki", 200, "No wiki pages yet"},
		{"/settings/assistant", 200, "Default effort"},
		{"/settings/usage", 200, "No usage yet"},
		{"/settings/subagents", 200, `data-agent="mail"`},
		{"/settings/notifications", 200, "Channels"},
	} {
		status, body, _ := fetch(t, client, "GET", base+tc.path, nil)
		if status != tc.status || !strings.Contains(body, tc.want) {
			t.Errorf("GET %s = %d, want %d with %q: %.300s", tc.path, status, tc.status, tc.want, body)
		}
		if strings.HasPrefix(tc.path, "/settings/") && !strings.Contains(body, `href="/settings/notifications"`) {
			t.Errorf("GET %s has no notifications tab", tc.path)
		}
	}
	if status, _, header := fetch(t, client, "GET", base+"/settings", nil); status != http.StatusSeeOther || header.Get("Location") != "/settings/assistant" {
		t.Errorf("GET /settings = %d %q", status, header.Get("Location"))
	}
	made := url.Values{"name": {"Kitchen"}, "effort": {"high"}, "context_limit": {"200000"}}
	if status, _, header := fetch(t, client, "POST", base+"/projects", made); status != http.StatusSeeOther || header.Get("Location") != "/projects/1/edit" {
		t.Errorf("POST /projects = %d %q", status, header.Get("Location"))
	}
	if status, body, _ := fetch(t, client, "GET", base+"/projects/1/edit", nil); status != http.StatusOK || !strings.Contains(body, "Kitchen") {
		t.Errorf("GET /projects/1/edit = %d: %.300s", status, body)
	}
	if status, _, _ := fetch(t, client, "GET", base+"/scheduled/new", nil); status != http.StatusNotFound && status != http.StatusMethodNotAllowed {
		t.Errorf("GET /scheduled/new = %d, want 404 or 405", status)
	}
}

package main

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
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

// startServe runs `atrium serve --auth local` on a free port over empty state and returns its base URL; the server
// is stopped, and its exit code checked, when the test ends.
func startServe(t *testing.T) string {
	t.Helper()
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
	t.Cleanup(func() {
		cancel()
		select {
		case code := <-done:
			if code != cli.ExitOK {
				t.Errorf("exit code %d", code)
			}
		case <-time.After(15 * time.Second):
			t.Error("serve did not stop on cancel")
		}
	})
	base := "http://127.0.0.1:" + port
	for range 100 {
		if resp, err := http.Get(base + "/healthz"); err == nil {
			_ = resp.Body.Close()
			return base
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("serve did not start")
	return ""
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

func TestServeRendersNotificationsPage(t *testing.T) {
	base := startServe(t)
	status, body, _ := fetch(t, http.DefaultClient, "GET", base+"/settings/notifications", nil)
	if status != http.StatusOK || !strings.Contains(body, "No notifications") {
		t.Errorf("GET /settings/notifications = %d: %s", status, body)
	}
}

func TestServeRendersMailPages(t *testing.T) {
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
	base := startServe(t)
	for _, path := range []string{"/proposals", "/activity", "/drafts", "/loops", "/watches"} {
		if status, _, _ := fetch(t, http.DefaultClient, "GET", base+path, nil); status != http.StatusOK {
			t.Errorf("GET %s = %d", path, status)
		}
	}
}

func TestServeRendersAssistantPages(t *testing.T) {
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
		{"/projects", 200, "No projects yet"},
		{"/projects/new", 200, "New project"},
		{"/projects/1", 404, "Nothing here"},
		{"/scheduled", 200, "Every day at 02:30"},
		{"/scheduled/new", 200, "New schedule"},
		{"/settings/assistant", 200, "Default effort"},
		{"/settings/usage", 200, "No usage yet"},
		{"/settings/skills", 200, `data-skill="wiki-capture"`},
		{"/settings/subagents", 200, `data-agent="mail"`},
		{"/settings/notifications", 200, "No notifications"},
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
	if status, _, header := fetch(t, client, "POST", base+"/projects", made); status != http.StatusSeeOther || header.Get("Location") != "/projects/1" {
		t.Errorf("POST /projects = %d %q", status, header.Get("Location"))
	}
	if status, body, _ := fetch(t, client, "GET", base+"/projects/1", nil); status != http.StatusOK || !strings.Contains(body, "Kitchen") {
		t.Errorf("GET /projects/1 = %d: %.300s", status, body)
	}
}

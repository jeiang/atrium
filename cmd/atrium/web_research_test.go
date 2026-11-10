package main

import (
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/assistant/webfetch"
)

const (
	pageAMarker = "The opening hours are listed on page B."
	pageBText   = "The library opens at nine."
)

type pageLog struct {
	mu   sync.Mutex
	hits []string
}

func (p *pageLog) seen() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.hits)
}

func longPage(link string) string {
	var b strings.Builder
	b.WriteString(`<html><head><title>Page A</title></head><body><article>`)
	for i := range 200 {
		if i == 50 {
			fmt.Fprintf(&b, `<p>%s See <a href="%s">page B</a>.</p>`, pageAMarker, link)
			continue
		}
		fmt.Fprintf(&b, "<p>Paragraph %d describes shelf %d of the reading room collection.</p>", i, i)
	}
	b.WriteString(`</article></body></html>`)
	return b.String()
}

func webPages(t *testing.T) (*httptest.Server, *pageLog) {
	t.Helper()
	t.Cleanup(webfetch.UseAddresses(func(a netip.Addr) bool { return a.IsLoopback() }))
	log := &pageLog{}
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.mu.Lock()
		log.hits = append(log.hits, r.URL.Path)
		log.mu.Unlock()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		switch r.URL.Path {
		case "/a":
			_, _ = w.Write([]byte(longPage(srv.URL + "/b")))
		case "/b":
			_, _ = w.Write([]byte(`<html><head><title>Page B</title></head><body><p>` + pageBText + `</p></body></html>`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, log
}

func threadTaint(t *testing.T, dir string, thread int64) (private, web bool) {
	t.Helper()
	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
	defer func() { _ = d.Close() }()
	var marks int64
	if err := d.Read.QueryRow("SELECT marks FROM threads WHERE id = ?", thread).Scan(&marks); err != nil {
		t.Fatal(err)
	}
	return marks&14 != 0, marks&1 != 0
}

func parentOf(t *testing.T, dir string, child int64) int64 {
	t.Helper()
	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
	defer func() { _ = d.Close() }()
	var parent int64
	if err := d.Read.QueryRow("SELECT parent_thread_id FROM threads WHERE id = ?", child).Scan(&parent); err != nil {
		t.Fatal(err)
	}
	return parent
}

func TestMarkedChatAsksBeforeFetchingAndFetchesOnApproval(t *testing.T) {
	srv, pages := webPages(t)
	dir, api, f := moduleFixture(t, flagsExcept("", ""))
	warm(t, api)
	thread := latestThread(t, dir)
	base, _, _ := startServeApp(t, freshApp(f, nil), &lockedBuffer{})
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if status, _, _ := fetch(t, client, "GET", base+"/chat", nil); status != http.StatusOK {
		t.Fatalf("GET /chat = %d", status)
	}
	long := srv.URL + "/b?" + strings.Repeat("x", 220)
	start := len(api.Requests())
	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("f1", "web_fetch", map[string]any{"url": srv.URL + "/a"})))
	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("f2", "web_fetch", map[string]any{"url": long})))
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("read both")))
	text := "read " + srv.URL + "/a then " + long
	if status, body, _ := fetch(t, client, "POST", fmt.Sprintf("%s/chat/%d/messages", base, thread), url.Values{"text": {text}}); status >= http.StatusBadRequest {
		t.Fatalf("POST message = %d: %s", status, body)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
	defer func() { _ = d.Close() }()
	var proposal int64
	var state string
	for deadline := time.Now().Add(20 * time.Second); ; {
		proposal, state = 0, ""
		if err := d.Read.QueryRow("SELECT id FROM proposals WHERE kind = 'permission'").Scan(&proposal); err != nil {
			proposal = 0
		}
		_ = d.Read.QueryRow("SELECT state FROM runs WHERE thread_id = ? ORDER BY id DESC LIMIT 1", thread).Scan(&state)
		if proposal != 0 && state == "parked" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no parked permission request: proposal %d state %q", proposal, state)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if hits := pages.seen(); !slices.Equal(hits, []string{"/a"}) {
		t.Fatalf("page fetches before approval = %v, want /a only", hits)
	}
	status, page, _ := fetch(t, client, "GET", fmt.Sprintf("%s/chat/%d", base, thread), nil)
	if status != http.StatusOK || !strings.Contains(page, fmt.Sprintf(`id="needs-%d"`, proposal)) {
		t.Fatalf("the thread does not list the permission request: %d", status)
	}
	code, body := hxFetch(t, client, http.MethodGet, base+fmt.Sprintf("/proposals/%d/card", proposal), nil)
	if code != http.StatusOK || !strings.Contains(body, long) {
		t.Fatalf("the card does not show the full URL: %d\n%s", code, body)
	}
	if code, body = hxFetch(t, client, http.MethodPost, base+fmt.Sprintf("/proposals/%d/approve", proposal), url.Values{"all": {"1"}}); code != http.StatusOK {
		t.Fatalf("approve = %d\n%s", code, body)
	}
	for deadline := time.Now().Add(20 * time.Second); ; {
		if err := d.Read.QueryRow("SELECT state FROM runs WHERE thread_id = ? ORDER BY id DESC LIMIT 1", thread).Scan(&state); err != nil {
			t.Fatal(err)
		}
		if state == "done" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("run state after approval = %q, want done", state)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if hits := pages.seen(); !slices.Equal(hits, []string{"/a", "/b"}) {
		t.Errorf("page fetches after approval = %v, want /a then /b", hits)
	}
	requests := api.Requests()[start:]
	if last := requests[len(requests)-1]; !strings.Contains(string(last.Body), pageBText) {
		t.Errorf("the approved page did not reach the model: %s", last.Body)
	}
}

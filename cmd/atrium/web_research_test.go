package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/assistant/webfetch"
	"github.com/jeiang/atrium/internal/db"
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

var loopbackFetch struct {
	mu      sync.Mutex
	users   int
	restore func()
}

func allowLoopbackFetch(t *testing.T) {
	t.Helper()
	loopbackFetch.mu.Lock()
	if loopbackFetch.users == 0 {
		loopbackFetch.restore = webfetch.UseAddresses(func(a netip.Addr) bool { return a.IsLoopback() })
	}
	loopbackFetch.users++
	loopbackFetch.mu.Unlock()
	t.Cleanup(func() {
		loopbackFetch.mu.Lock()
		defer loopbackFetch.mu.Unlock()
		if loopbackFetch.users--; loopbackFetch.users == 0 {
			loopbackFetch.restore()
		}
	})
}

func webPages(t *testing.T) (*httptest.Server, *pageLog) {
	t.Helper()
	allowLoopbackFetch(t)
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
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), db.Key{})
	defer func() { _ = d.Close() }()
	var marks int64
	if err := d.Read.QueryRow("SELECT marks FROM threads WHERE id = ?", thread).Scan(&marks); err != nil {
		t.Fatal(err)
	}
	return marks&14 != 0, marks&1 != 0
}

func parentOf(t *testing.T, dir string, child int64) int64 {
	t.Helper()
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), db.Key{})
	defer func() { _ = d.Close() }()
	var parent int64
	if err := d.Read.QueryRow("SELECT parent_thread_id FROM threads WHERE id = ?", child).Scan(&parent); err != nil {
		t.Fatal(err)
	}
	return parent
}

func TestMarkedChatAsksBeforeFetchingAndFetchesOnApproval(t *testing.T) {
	t.Parallel()
	srv, pages := webPages(t)
	dir, api, f := moduleFixture(t, flagsExcept("", ""))
	warm(t, api)
	thread := latestThread(t, dir)
	base, _, _ := startServeApp(t, freshApp(f), &lockedBuffer{})
	client := webLocalG2(t, base)
	long := srv.URL + "/b?" + strings.Repeat("x", 220)
	start := len(api.Requests())
	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("f1", "web_fetch", map[string]any{"url": srv.URL + "/a"})))
	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("f2", "web_fetch", map[string]any{"url": long})))
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("read both")))
	text := "read " + srv.URL + "/a then " + long
	webOKG2(t, client.do(http.MethodPost, fmt.Sprintf("/web/v1/threads/%d/messages", thread), map[string]any{"text": text}))
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), db.Key{})
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
	if card := client.do(http.MethodGet, fmt.Sprintf("/web/v1/proposals/%d", proposal), nil); card.status != http.StatusOK || !strings.Contains(card.body, long) {
		t.Fatalf("the proposal does not show the full URL: %d\n%s", card.status, card.body)
	}
	webOKG2(t, client.do(http.MethodPost, fmt.Sprintf("/web/v1/approvals/%d/approve", proposal), nil))
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

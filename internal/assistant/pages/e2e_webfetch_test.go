package pages

import (
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/assistant/webfetch"
)

func TestWebFetchReadsAllowedPagesAndRefusesPrivateRedirects(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/page":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte("<html><head><title>Local page</title></head><body><p>Fetched body text.</p></body></html>"))
		case "/redirect":
			http.Redirect(w, r, "http://10.0.0.1/", http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	host, _, err := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	ip := netip.MustParseAddr(host)
	s := newSite(t, withWebfetch(&webfetch.Fetcher{Allowed: func(a netip.Addr) bool { return a == ip }}))

	s.api.Push(
		reply1(anthropictest.ToolUse("c1", "web_fetch", map[string]any{"url": server.URL + "/page"})),
		reply1(anthropictest.Text("read it")),
	)
	thread := s.startThread("fetch " + server.URL + "/page")
	s.settle(s.lastRun(thread))
	requests := s.api.Requests()
	if len(requests) != 2 {
		t.Fatalf("%d model requests, want 2", len(requests))
	}
	body := string(requests[1].Body)
	mustContain(t, body, "Fetched body text.", "Local page")
	mustNotContain(t, body, `"is_error":true`)

	s.api.Push(
		reply1(anthropictest.ToolUse("c2", "web_fetch", map[string]any{"url": server.URL + "/redirect"})),
		reply1(anthropictest.Text("blocked")),
	)
	thread = s.startThread("fetch " + server.URL + "/redirect")
	s.settle(s.lastRun(thread))
	requests = s.api.Requests()
	if len(requests) != 4 {
		t.Fatalf("%d model requests, want 4", len(requests))
	}
	body = string(requests[3].Body)
	mustContain(t, body, `"is_error":true`, "non-public address")
}

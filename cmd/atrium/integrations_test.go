package main

import (
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	grafanatools "github.com/jeiang/atrium/internal/assistant/tools/grafana"
	"github.com/jeiang/atrium/internal/connections/transport"
	"github.com/jeiang/atrium/internal/integrations"
)

type healthServer struct {
	mu     sync.Mutex
	status int
}

func (h *healthServer) handler(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()
	switch r.URL.Path {
	case "/api/user":
		w.WriteHeader(h.status)
		if h.status == http.StatusOK {
			_, _ = io.WriteString(w, `{"login":"viewer"}`)
		}
	case "/api/search":
		_, _ = io.WriteString(w, `[]`)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (h *healthServer) set(status int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.status = status
}

func highPriority(t *testing.T, push *fakePush, browser pushBrowser) []string {
	t.Helper()
	push.mu.Lock()
	received := slices.Clone(push.received)
	push.mu.Unlock()
	var out []string
	for _, r := range received {
		if r.header.Get("Urgency") == "high" {
			out = append(out, fmt.Sprint(browser.decrypt(t, r.body)["title"]))
		}
	}
	return out
}

func unsetEnv(t *testing.T, names ...string) {
	t.Helper()
	for _, name := range names {
		if f := fixtureFor(t); f != nil {
			f.unset(name)
		}
		setEnv(t, name, "")
	}
}

type integrationSite struct {
	t      *testing.T
	dir    string
	base   string
	client *http.Client
	stop   func()
}

func (s *integrationSite) start() {
	s.t.Helper()
	s.base, _, s.stop = startServeStoppable(s.t, s.dir)
	jar, _ := cookiejar.New(nil)
	s.client = &http.Client{Jar: jar}
}

func (s *integrationSite) page() string {
	s.t.Helper()
	code, body, _ := fetch(s.t, s.client, http.MethodGet, s.base+"/settings/integrations", nil)
	if code != http.StatusOK {
		s.t.Fatalf("integrations page: %d", code)
	}
	return body
}

func (s *integrationSite) post(path string, form url.Values) (int, string) {
	s.t.Helper()
	s.page()
	if form == nil {
		form = url.Values{}
	}
	code, body, _ := fetch(s.t, s.client, http.MethodPost, s.base+"/settings/integrations"+path, form)
	return code, body
}

func (s *integrationSite) conn(path string, form url.Values) (int, string) {
	s.t.Helper()
	s.page()
	if form == nil {
		form = url.Values{}
	}
	code, body, _ := fetch(s.t, s.client, http.MethodPost, s.base+"/settings/connections"+path, form)
	return code, body
}

func connectionState(t *testing.T, dir, id string) string {
	t.Helper()
	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
	var state string
	if err := d.Read.QueryRow("SELECT state FROM connections WHERE id = ?", id).Scan(&state); err != nil {
		return ""
	}
	return state
}

func health(t *testing.T, dir, name string) string {
	t.Helper()
	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
	var state string
	if err := d.Read.QueryRow("SELECT state FROM integration_health WHERE name = ?", name).Scan(&state); err != nil {
		return integrations.Untested
	}
	return state
}

func storedURL(t *testing.T, dir string) string {
	t.Helper()
	v, _ := storedSetting(t, filepath.Join(dir, "atrium.db"), grafanatools.KeyURL)
	return v
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(25 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatal("condition not reached")
}

func TestIntegrationTransportTrustsOnlyTheConfiguredCAAndUsesTheProxy(t *testing.T) {
	t.Parallel()
	tlsHealth := &healthServer{status: http.StatusOK}
	tlsSrv := httptest.NewTLSServer(http.HandlerFunc(tlsHealth.handler))
	t.Cleanup(tlsSrv.Close)
	var mu sync.Mutex
	var proxied []string
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		proxied = append(proxied, r.Method+" "+r.Host)
		mu.Unlock()
		if r.Method != http.MethodConnect {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		upstream, err := net.Dial("tcp", r.Host)
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusOK)
		conn, buf, err := w.(http.Hijacker).Hijack()
		if err != nil {
			_ = upstream.Close()
			return
		}
		go func() { _, _ = io.Copy(upstream, buf); _ = upstream.Close() }()
		go func() { _, _ = io.Copy(conn, upstream); _ = conn.Close() }()
	}))
	t.Cleanup(proxy.Close)

	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	setEnv(t, settingEnv(grafanatools.KeyURL), "")
	s := &integrationSite{t: t, dir: dir}
	s.start()

	if code, body := s.post("/grafana/test", url.Values{"url": {tlsSrv.URL}}); code != http.StatusUnprocessableEntity || !strings.Contains(body, "certificate") {
		t.Fatalf("unknown authority: %d, want a TLS failure", code)
	}
	ca := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: tlsSrv.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := runSettings(t, "set", transport.CAKey("grafana"), filepath.Join(dir, "missing.pem")); got.code != 0 {
		t.Fatalf("set CA: %d %q", got.code, got.stderr)
	}
	if code, body := s.post("/grafana/test", url.Values{"url": {tlsSrv.URL}}); code != http.StatusUnprocessableEntity || !strings.Contains(body, "cannot be read") {
		t.Fatalf("unreadable CA: %d", code)
	}
	if got := runSettings(t, "set", transport.CAKey("grafana"), ca); got.code != 0 {
		t.Fatalf("set CA: %d %q", got.code, got.stderr)
	}
	if got := runSettings(t, "set", transport.ProxyKey("grafana"), proxy.URL); got.code != 0 {
		t.Fatalf("set proxy: %d %q", got.code, got.stderr)
	}
	if code, _ := s.post("/grafana/test", url.Values{"url": {tlsSrv.URL}}); code != http.StatusOK {
		t.Fatalf("private CA through the proxy: %d", code)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(proxied) == 0 || !strings.HasPrefix(proxied[0], "CONNECT "+strings.TrimPrefix(tlsSrv.URL, "https://")) {
		t.Errorf("proxy saw %v, want a CONNECT to the server", proxied)
	}
}

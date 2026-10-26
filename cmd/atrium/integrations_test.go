package main

import (
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/integrations"
	"github.com/jeiang/atrium/internal/integrations/transport"
	"github.com/jeiang/atrium/internal/media"
	"github.com/jeiang/atrium/internal/notify"
)

type healthServer struct {
	mu        sync.Mutex
	status    int
	published []string
}

func (h *healthServer) handler(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()
	switch {
	case r.URL.Path == "/v1/health":
		w.WriteHeader(h.status)
	case r.Method == http.MethodGet:
		w.WriteHeader(http.StatusForbidden)
	case r.Method == http.MethodPost:
		raw, _ := io.ReadAll(r.Body)
		h.published = append(h.published, string(raw))
	}
}

func (h *healthServer) set(status int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.status = status
}

func (h *healthServer) notices() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, p := range h.published {
		if strings.Contains(p, `"priority":4`) {
			out = append(out, p)
		}
	}
	return out
}

func unsetEnv(t *testing.T, names ...string) {
	t.Helper()
	for _, name := range names {
		t.Setenv(name, "")
		_ = os.Unsetenv(name)
	}
}

type integrationSite struct {
	t      *testing.T
	dir    string
	base   string
	client *http.Client
}

func (s *integrationSite) start() {
	s.t.Helper()
	s.base = startServeIn(s.t, s.dir)
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
	v, _ := storedSetting(t, filepath.Join(dir, "atrium.db"), notify.KeyURL)
	return v
}

func TestIntegrationTestSavesOnlyWorkingConfigAndHealthNotifiesOncePerChange(t *testing.T) {
	good := &healthServer{status: http.StatusOK}
	goodSrv := httptest.NewServer(http.HandlerFunc(good.handler))
	t.Cleanup(goodSrv.Close)
	bad := &healthServer{status: http.StatusInternalServerError}
	badSrv := httptest.NewServer(http.HandlerFunc(bad.handler))
	t.Cleanup(badSrv.Close)

	dir, _ := moduleFixture(t, flagsExcept("", ""))
	t.Setenv(settingEnv(notify.KeyURL), "")
	t.Setenv(settingEnv(media.KeyQbitURL), "")
	s := &integrationSite{t: t, dir: dir}

	t.Run("a failed test keeps the stored config", func(t *testing.T) {
		s.t = t
		s.start()
		page := s.page()
		if !strings.Contains(page, `data-integration="ntfy" data-state="untested"`) || !strings.Contains(page, `id="url-ntfy" name="url"`) {
			t.Fatal("ntfy is not listed as an untested editable integration")
		}
		if !strings.Contains(page, `id="url-qbittorrent" class="break-all font-mono text-xs" data-pinned`) {
			t.Error("the deployment-only qBittorrent URL is editable")
		}
		if code, body := s.post("/ntfy/test", url.Values{"url": {badSrv.URL}}); code != http.StatusUnprocessableEntity || !strings.Contains(body, "test failed (failing)") {
			t.Fatalf("failing test: %d", code)
		}
		if got := storedURL(t, dir); got != "" {
			t.Fatalf("a failed test stored %q", got)
		}
		if code, _ := s.post("/ntfy/test", url.Values{"url": {goodSrv.URL}}); code != http.StatusOK {
			t.Fatalf("passing test: %d", code)
		}
		if got := storedURL(t, dir); got != goodSrv.URL || health(t, dir, "ntfy") != integrations.OK {
			t.Fatalf("after a passing test: url %q state %s", got, health(t, dir, "ntfy"))
		}
		if code, body := s.post("/ntfy/test", url.Values{"url": {badSrv.URL}}); code != http.StatusUnprocessableEntity || !strings.Contains(body, "test failed") {
			t.Fatalf("failing test over a working one: %d", code)
		}
		if got := storedURL(t, dir); got != goodSrv.URL || health(t, dir, "ntfy") != integrations.OK {
			t.Errorf("a failed test replaced the working record: url %q state %s", got, health(t, dir, "ntfy"))
		}
	})

	t.Run("the daily check moves through failing and needs re-auth", func(t *testing.T) {
		s.t = t
		good.set(http.StatusInternalServerError)
		s.start()
		waitFor(t, func() bool { return health(t, dir, "ntfy") == integrations.Failing })
		if !strings.Contains(s.page(), "data-integration-alerts") {
			t.Error("no admin banner for a failing integration")
		}
		waitFor(t, func() bool { return len(good.notices()) == 1 })
		if n := good.notices(); !strings.Contains(n[0], `"priority":4`) {
			t.Errorf("state-change notice %q, want one high-priority system message", n[0])
		}
	})

	t.Run("an unchanged state sends nothing", func(t *testing.T) {
		s.t = t
		s.start()
		time.Sleep(300 * time.Millisecond)
		if n := len(good.notices()); n != 1 {
			t.Errorf("%d notices after a restart in the same state, want 1", n)
		}
	})

	t.Run("refused credentials need re-auth", func(t *testing.T) {
		s.t = t
		good.set(http.StatusUnauthorized)
		s.start()
		waitFor(t, func() bool { return health(t, dir, "ntfy") == integrations.NeedsReauth })
		waitFor(t, func() bool { return len(good.notices()) == 2 })
	})

	t.Run("disconnect unsets the record and keeps the data", func(t *testing.T) {
		s.t = t
		good.set(http.StatusOK)
		s.start()
		if code, _ := s.post("/ntfy/disconnect", nil); code != http.StatusOK {
			t.Fatalf("disconnect: %d", code)
		}
		if got := storedURL(t, dir); got != "" || health(t, dir, "ntfy") != integrations.Untested {
			t.Fatalf("after disconnect: url %q state %s", got, health(t, dir, "ntfy"))
		}
		restarted := startServeIn(t, dir)
		if code, _, _ := fetch(t, http.DefaultClient, http.MethodGet, restarted+"/settings/notifications", nil); code != http.StatusNotFound {
			t.Errorf("notifications page after disconnect and restart: %d, want 404 (needs settings)", code)
		}
		d := mustConnect(t, filepath.Join(dir, "atrium.db"))
		var kept int
		if err := d.Read.QueryRow("SELECT COUNT(*) FROM notifications").Scan(&kept); err != nil || kept == 0 {
			t.Errorf("notifications after disconnect: %d (%v), want the rows kept", kept, err)
		}
	})
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

	dir, _ := moduleFixture(t, flagsExcept("", ""))
	t.Setenv(settingEnv(notify.KeyURL), "")
	s := &integrationSite{t: t, dir: dir}
	s.start()

	if code, body := s.post("/ntfy/test", url.Values{"url": {tlsSrv.URL}}); code != http.StatusUnprocessableEntity || !strings.Contains(body, "certificate") {
		t.Fatalf("unknown authority: %d, want a TLS failure", code)
	}
	ca := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: tlsSrv.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := runSettings(t, "set", transport.CAKey("ntfy"), filepath.Join(dir, "missing.pem")); got.code != 0 {
		t.Fatalf("set CA: %d %q", got.code, got.stderr)
	}
	if code, body := s.post("/ntfy/test", url.Values{"url": {tlsSrv.URL}}); code != http.StatusUnprocessableEntity || !strings.Contains(body, "cannot be read") {
		t.Fatalf("unreadable CA: %d", code)
	}
	if got := runSettings(t, "set", transport.CAKey("ntfy"), ca); got.code != 0 {
		t.Fatalf("set CA: %d %q", got.code, got.stderr)
	}
	if got := runSettings(t, "set", transport.ProxyKey("ntfy"), proxy.URL); got.code != 0 {
		t.Fatalf("set proxy: %d %q", got.code, got.stderr)
	}
	if code, _ := s.post("/ntfy/test", url.Values{"url": {tlsSrv.URL}}); code != http.StatusOK {
		t.Fatalf("private CA through the proxy: %d", code)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(proxied) == 0 || !strings.HasPrefix(proxied[0], "CONNECT "+strings.TrimPrefix(tlsSrv.URL, "https://")) {
		t.Errorf("proxy saw %v, want a CONNECT to the server", proxied)
	}
}

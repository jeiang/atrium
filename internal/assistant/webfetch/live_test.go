package webfetch

import (
	"context"
	"crypto/x509"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var loopback = netip.MustParseAddr("127.0.0.1")

func onlyLoopback(ip netip.Addr) bool { return ip == loopback }

const page = "<html><head><title>Alpha Guide</title></head><body><nav>menu</nav><p>%s</p><script>x()</script></body></html>"

var paragraph = strings.Repeat("The widget calibration procedure takes three steps and needs a torque wrench. ", 3)

func reply(w http.ResponseWriter, status int, body []byte, kind string, headers ...string) {
	w.Header().Set("content-type", kind)
	w.Header().Set("content-length", strconv.Itoa(len(body)))
	for i := 0; i+1 < len(headers); i += 2 {
		w.Header().Set(headers[i], headers[i+1])
	}
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func handler(seen *sync.Map) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if seen != nil {
			seen.Store(r.URL.Path, r.Header.Clone())
			seen.Store("host", r.Host)
		}
		switch r.URL.Path {
		case "/ok":
			reply(w, 200, []byte(strings.Replace(page, "%s", paragraph, 1)), "text/html")
		case "/big":
			reply(w, 200, []byte(strings.Repeat("a", MaxBytes+1)), "text/plain")
		case "/chunked-big":
			w.Header().Set("content-type", "text/plain")
			w.WriteHeader(200)
			block := []byte(strings.Repeat("a", 65536))
			for range MaxBytes/65536 + 2 {
				if _, err := w.Write(block); err != nil {
					return
				}
				w.(http.Flusher).Flush()
			}
		case "/slow":
			w.Header().Set("content-type", "text/plain")
			w.WriteHeader(200)
			for range 50 {
				if _, err := w.Write([]byte("a")); err != nil {
					return
				}
				w.(http.Flusher).Flush()
				time.Sleep(100 * time.Millisecond)
			}
		case "/stall":
			time.Sleep(3 * time.Second)
		case "/binary":
			reply(w, 200, []byte{0, 1}, "application/octet-stream")
		case "/hop":
			reply(w, 302, nil, "text/html", "location", "/ok")
		case "/loop":
			reply(w, 302, nil, "text/html", "location", "/loop")
		case "/to-private":
			reply(w, 302, nil, "text/html", "location", "http://192.168.1.1/secret")
		case "/to-metadata":
			reply(w, 302, nil, "text/html", "location", "http://169.254.169.254/latest")
		case "/to-file":
			reply(w, 302, nil, "text/html", "location", "file:///etc/passwd")
		case "/to-same-host":
			reply(w, 302, nil, "text/html", "location", "/ok")
		default:
			reply(w, 404, nil, "text/html")
		}
	})
}

func serve(t *testing.T, seen *sync.Map) (base string, port int) {
	t.Helper()
	srv := httptest.NewServer(handler(seen))
	t.Cleanup(srv.Close)
	_, p, _ := net.SplitHostPort(srv.Listener.Addr().String())
	port, _ = strconv.Atoi(p)
	return srv.URL, port
}

func loopbackFetcher() *Fetcher { return &Fetcher{Allowed: onlyLoopback} }

func TestLiveFetchOKAndRedirect(t *testing.T) {
	base, _ := serve(t, nil)
	p, err := loopbackFetcher().FetchPage(t.Context(), base+"/hop")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(p.Body), "calibration") || p.Charset != "utf-8" || !strings.HasSuffix(p.URL, "/ok") {
		t.Fatalf("page %q %q %q", p.Body, p.Charset, p.URL)
	}
	title, text := ExtractText(string(p.Body), p.URL)
	if title != "Alpha Guide" || !strings.Contains(text, "calibration") || strings.Contains(text, "menu") {
		t.Errorf("ExtractText = %q, %q", title, text)
	}
}

func TestLiveRequestHeaders(t *testing.T) {
	var seen sync.Map
	base, port := serve(t, &seen)
	if _, err := loopbackFetcher().FetchPage(t.Context(), base+"/ok?q=1"); err != nil {
		t.Fatal(err)
	}
	got, _ := seen.Load("/ok")
	h := got.(http.Header)
	for name, want := range map[string]string{"User-Agent": UserAgent, "Accept": "text/html, application/xhtml+xml, text/plain", "Accept-Encoding": "identity"} {
		if h.Get(name) != want {
			t.Errorf("%s = %q, want %q", name, h.Get(name), want)
		}
	}
	if host, _ := seen.Load("host"); host != "127.0.0.1:"+strconv.Itoa(port) {
		t.Errorf("Host = %v", host)
	}
	if h.Get("Cookie") != "" || h.Get("Authorization") != "" {
		t.Errorf("credentials sent: %v", h)
	}
}

func TestLiveRedirectToNonPublicIsBlocked(t *testing.T) {
	base, _ := serve(t, nil)
	for _, path := range []string{"/to-private", "/to-metadata", "/to-file"} {
		_, err := loopbackFetcher().FetchPage(t.Context(), base+path)
		var blocked Blocked
		if !errors.As(err, &blocked) {
			t.Errorf("%s: %v, want Blocked", path, err)
		}
	}
}

func TestLiveRedirectLimit(t *testing.T) {
	base, _ := serve(t, nil)
	_, err := loopbackFetcher().FetchPage(t.Context(), base+"/loop")
	if err == nil || !strings.Contains(err.Error(), "redirects") {
		t.Errorf("error %v", err)
	}
}

func TestLiveSizeLimit(t *testing.T) {
	base, _ := serve(t, nil)
	for _, path := range []string{"/big", "/chunked-big"} {
		_, err := loopbackFetcher().FetchPage(t.Context(), base+path)
		if err == nil || err.Error() != "too large" {
			t.Errorf("%s: %v", path, err)
		}
	}
}

func TestLiveTimeout(t *testing.T) {
	base, _ := serve(t, nil)
	f := loopbackFetcher()
	f.Timeout = time.Second
	for path, want := range map[string]string{"/slow": "timeout", "/stall": "timed out"} {
		started := time.Now()
		_, err := f.FetchPage(t.Context(), base+path)
		var fe FetchError
		if !errors.As(err, &fe) || err.Error() != want {
			t.Errorf("%s: error %v, want %q", path, err, want)
		}
		if time.Since(started) > 2500*time.Millisecond {
			t.Errorf("%s took %v", path, time.Since(started))
		}
	}
}

func TestLiveRejectsNonTextAndErrors(t *testing.T) {
	base, _ := serve(t, nil)
	for path, want := range map[string]string{"/binary": "content type application/octet-stream", "/missing": "status 404"} {
		_, err := loopbackFetcher().FetchPage(t.Context(), base+path)
		var fe FetchError
		if !errors.As(err, &fe) || err.Error() != want {
			t.Errorf("%s: error %v, want %q", path, err, want)
		}
	}
}

func TestLiveCancel(t *testing.T) {
	base, _ := serve(t, nil)
	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(200*time.Millisecond, cancel)
	_, err := loopbackFetcher().FetchPage(ctx, base+"/stall")
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error %v", err)
	}
}

func TestDefaultGuardRefusesNonPublicLiteralsWithoutConnecting(t *testing.T) {
	var connected atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { connected.Store(true) }))
	defer srv.Close()
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	for _, host := range []string{"127.0.0.1", "[::1]", "[::ffff:127.0.0.1]", "localhost", "0.0.0.0", "0x7f.1", "2130706433"} {
		_, err := FetchPage(t.Context(), "http://"+host+":"+port+"/")
		if err == nil {
			t.Errorf("%s fetched", host)
		}
	}
	if connected.Load() {
		t.Error("the server was reached")
	}
}

// The first answer is vetted and then connected to without a second lookup; a later answer that would be
// loopback or private never matters, and a redirect to the same name is looked up again and refused.
func TestDNSRebindingIsDefeatedByPinning(t *testing.T) {
	base, port := serve(t, nil)
	_ = base
	var lookups atomic.Int32
	f := &Fetcher{
		Allowed: onlyLoopback,
		Lookup: func(_ context.Context, host string) ([]netip.Addr, error) {
			if lookups.Add(1) == 1 {
				return []netip.Addr{loopback}, nil
			}
			return []netip.Addr{netip.MustParseAddr("10.0.0.1")}, nil
		},
	}
	p, err := f.FetchPage(t.Context(), "http://flip.test:"+strconv.Itoa(port)+"/ok")
	if err != nil || !strings.Contains(string(p.Body), "calibration") {
		t.Fatalf("pinned fetch: %v", err)
	}
	if lookups.Load() != 1 {
		t.Errorf("%d lookups for one hop", lookups.Load())
	}
	lookups.Store(0)
	_, err = f.FetchPage(t.Context(), "http://flip.test:"+strconv.Itoa(port)+"/to-same-host")
	var blocked Blocked
	if !errors.As(err, &blocked) || err.Error() != "flip.test resolves to a non-public address" {
		t.Errorf("rebound redirect: %v", err)
	}
	if lookups.Load() != 2 {
		t.Errorf("%d lookups for two hops", lookups.Load())
	}
}

func TestDialChecksTheAddressItConnectsTo(t *testing.T) {
	var connected atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { connected.Store(true) }))
	defer srv.Close()
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	p, _ := strconv.Atoi(port)
	var f Fetcher
	_, err := f.dial(t.Context(), Dest{Scheme: "http", Host: "liar.test", Port: p, Address: loopback})
	var blocked Blocked
	if !errors.As(err, &blocked) {
		t.Errorf("dial to loopback with the default guard: %v", err)
	}
	if connected.Load() {
		t.Error("the server was reached")
	}
}

func TestLiveTLSVerifiesTheURLHostAtThePinnedAddress(t *testing.T) {
	srv := httptest.NewUnstartedServer(handler(nil))
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	srv.StartTLS()
	defer srv.Close()
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	roots := x509.NewCertPool()
	roots.AddCert(srv.Certificate())
	f := &Fetcher{
		Allowed:  onlyLoopback,
		Lookup:   func(context.Context, string) ([]netip.Addr, error) { return []netip.Addr{loopback}, nil },
		tlsRoots: roots,
	}
	p, err := f.FetchPage(t.Context(), "https://example.com:"+port+"/ok")
	if err != nil || !strings.Contains(string(p.Body), "calibration") {
		t.Fatalf("https fetch: %v", err)
	}
	_, err = f.FetchPage(t.Context(), "https://wrong.test:"+port+"/ok")
	var fe FetchError
	if !errors.As(err, &fe) || !strings.Contains(err.Error(), "certificate") {
		t.Errorf("wrong host name: %v", err)
	}
	f.tlsRoots = nil
	_, err = f.FetchPage(t.Context(), "https://example.com:"+port+"/ok")
	if !errors.As(err, &fe) || !strings.Contains(err.Error(), "certificate") {
		t.Errorf("untrusted certificate: %v", err)
	}
}

package web

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

const (
	groupHeader = "admins, atrium_access"
	testHost    = "testserver"
)

func stubModule(r chi.Router, d *Deps) {
	r.Get("/chat", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "chat "+CSRFToken(r.Context()))
	})
	r.Post("/chat", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/chat/1", http.StatusSeeOther)
	})
	r.Delete("/rules/{id}", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	r.Get("/big", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, strings.Repeat("a", 2000))
	})
	r.Get("/small", func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "tiny") })
	r.Get("/boom", func(w http.ResponseWriter, r *http.Request) { panic("boom") })
	r.Get("/missing", func(w http.ResponseWriter, r *http.Request) { d.Error(w, r, http.StatusNotFound, "unknown message") })
	r.Get("/conflict", func(w http.ResponseWriter, r *http.Request) {
		d.Error(w, r, http.StatusConflict, "subagent threads are read-only")
	})
	r.Get("/slash/", func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "slash") })
}

func newServer(t *testing.T, auth string, hosts []string, mutate ...func(*Options)) *Server {
	t.Helper()
	o := Options{Auth: auth, AllowedHosts: hosts, Modules: []Module{stubModule}}
	for _, m := range mutate {
		m(&o)
	}
	s, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

type client struct {
	t       *testing.T
	srv     http.Handler
	host    string
	cookie  string
	headers http.Header
}

func newClient(t *testing.T, srv http.Handler) *client {
	return &client{t: t, srv: srv, host: testHost, headers: http.Header{}}
}

func (c *client) do(method, path string, body io.Reader, headers ...[2]string) *httptest.ResponseRecorder {
	c.t.Helper()
	req := httptest.NewRequest(method, path, body)
	req.Host = c.host
	for k, v := range c.headers {
		req.Header[k] = v
	}
	if c.cookie != "" {
		req.Header.Set("Cookie", c.cookie)
	}
	for _, h := range headers {
		req.Header.Set(h[0], h[1])
	}
	rec := httptest.NewRecorder()
	c.srv.ServeHTTP(rec, req)
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == CSRFCookie {
			c.cookie = ck.Name + "=" + ck.Value
		}
	}
	return rec
}

func (c *client) get(path string, headers ...[2]string) *httptest.ResponseRecorder {
	return c.do(http.MethodGet, path, nil, headers...)
}

func (c *client) token() string { return strings.TrimPrefix(c.cookie, CSRFCookie+"=") }

func (c *client) postForm(path string, form url.Values, headers ...[2]string) *httptest.ResponseRecorder {
	headers = append(headers, [2]string{"Content-Type", formType})
	return c.do(http.MethodPost, path, strings.NewReader(form.Encode()), headers...)
}

func local(t *testing.T) *client {
	c := newClient(t, newServer(t, AuthLocal, []string{testHost}))
	c.get("/chat")
	c.headers.Set(CSRFHeader, c.token())
	return c
}

func wantStatus(t *testing.T, rec *httptest.ResponseRecorder, want int, label string) {
	t.Helper()
	if rec.Code != want {
		t.Errorf("%s: status = %d, want %d (body %q)", label, rec.Code, want, rec.Body.String())
	}
}

func TestProxyAuthRequiresGroupExceptStatic(t *testing.T) {
	c := newClient(t, newServer(t, AuthProxy, []string{testHost}))
	wantStatus(t, c.get("/chat"), 403, "no group")
	wantStatus(t, c.get("/chat", [2]string{"Remote-Groups", "users"}), 403, "other group")
	wantStatus(t, c.get("/chat", [2]string{"Remote-Groups", "atrium_access_x"}), 403, "prefix")
	wantStatus(t, c.get("/chat", [2]string{"Remote-Groups", "x,atrium_access"}), 200, "comma list")
	wantStatus(t, c.get("/chat", [2]string{"Remote-Groups", groupHeader}), 200, "group")
	wantStatus(t, c.get("/static/app.css"), 200, "static without group")
	if body := c.get("/chat").Body.String(); body != "Forbidden" {
		t.Errorf("denied body = %q", body)
	}
}

func TestHostAllowlist(t *testing.T) {
	group := [2]string{"Remote-Groups", groupHeader}
	proxy := newClient(t, newServer(t, AuthProxy, []string{"atrium.test"}))
	for host, want := range map[string]int{"atrium.test": 200, "ATRIUM.test": 200, "atrium.test:8443": 200, "evil.test": 400, "": 400} {
		proxy.host = host
		wantStatus(t, proxy.get("/chat", group), want, "chat host "+host)
	}
	proxy.host = "evil.test"
	wantStatus(t, proxy.get("/static/app.css"), 400, "static host check")
	wantStatus(t, proxy.do(http.MethodPost, "/chat", nil, group), 400, "forged group cannot stand in for host")

	unconfigured := newClient(t, newServer(t, AuthProxy, nil))
	wantStatus(t, unconfigured.get("/chat", group), 400, "unconfigured proxy")

	loc := newClient(t, newServer(t, AuthLocal, nil))
	for _, good := range []string{"localhost", "localhost:8481", "127.0.0.1:8481", "[::1]:8481", "[::1]", "LOCALHOST"} {
		loc.host = good
		wantStatus(t, loc.get("/chat"), 200, good)
	}
	for _, bad := range []string{"evil.test:8481", "testserver", "127.0.0.1.evil.test", "localhost.evil.test", "::1", "[::1", "", "[::2]:80", "0.0.0.0"} {
		loc.host = bad
		wantStatus(t, loc.get("/chat"), 400, bad)
	}
	if body := loc.get("/chat").Body.String(); body != "Bad Host header" {
		t.Errorf("body = %q", body)
	}
}

func TestCustomHostsReplaceLocalDefaults(t *testing.T) {
	c := newClient(t, newServer(t, AuthLocal, []string{"atrium.test"}))
	c.host = "localhost"
	wantStatus(t, c.get("/chat"), 400, "default host replaced")
	c.host = "atrium.test"
	wantStatus(t, c.get("/chat"), 200, "configured host")
}

func TestHostWithAbsoluteRequestTarget(t *testing.T) {
	srv := newServer(t, AuthLocal, nil)
	req := httptest.NewRequest(http.MethodGet, "http://evil.test/chat", nil)
	req.Host = "evil.test"
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	wantStatus(t, rec, 400, "absolute-form target")
}

func TestHealthzBypassesHostAndGroup(t *testing.T) {
	c := newClient(t, newServer(t, AuthProxy, []string{"atrium.test"}))
	c.host = "127.0.0.1:8480"
	rec := c.get("/healthz")
	wantStatus(t, rec, 200, "probe")
	if rec.Body.Len() != 0 {
		t.Errorf("body = %q, want empty", rec.Body.String())
	}
	if len(rec.Result().Cookies()) != 0 || rec.Header().Get("Set-Cookie") != "" {
		t.Error("healthz set a cookie")
	}
	if rec.Header().Get("Content-Security-Policy") != CSP || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("healthz lacks security headers: %v", rec.Header())
	}
	wantStatus(t, c.do(http.MethodPost, "/healthz", nil), 400, "POST is not exempt")
	wantStatus(t, c.do(http.MethodHead, "/healthz", nil), 400, "HEAD is not exempt")
	c.host = "atrium.test"
	wantStatus(t, c.do(http.MethodPost, "/healthz", nil, [2]string{"Remote-Groups", groupHeader}), 403, "POST needs a token")
}

func TestLocalAuthSkipsGroupCheck(t *testing.T) {
	wantStatus(t, newClient(t, newServer(t, AuthLocal, []string{testHost})).get("/chat"), 200, "local")
	if _, err := New(Options{Auth: "open"}); err == nil {
		t.Error("unknown auth mode accepted")
	}
}

func TestPublicHookSkipsGroupNotHost(t *testing.T) {
	public := func(o *Options) {
		o.Public = func(r *http.Request) bool { return strings.HasPrefix(r.URL.Path, "/chat") }
	}
	c := newClient(t, newServer(t, AuthProxy, []string{testHost}, public))
	wantStatus(t, c.get("/chat"), 200, "public path")
	wantStatus(t, c.get("/small"), 403, "non-public path")
	c.host = "evil.test"
	wantStatus(t, c.get("/chat"), 400, "public path still host-checked")
}

func TestCSRFRejectsUnknownTokensAndCrossOrigin(t *testing.T) {
	c := newClient(t, newServer(t, AuthLocal, []string{testHost}))
	form := url.Values{"text": {""}}
	wantStatus(t, c.postForm("/chat", form), 403, "no cookie yet")
	first := c.get("/chat")
	token := c.token()
	setCookie := first.Header().Get("Set-Cookie")
	for _, attr := range []string{"HttpOnly", "SameSite=Strict", "Path=/"} {
		if !strings.Contains(setCookie, attr) {
			t.Errorf("Set-Cookie %q lacks %s", setCookie, attr)
		}
	}
	if strings.Contains(setCookie, "Secure") {
		t.Errorf("plain http cookie is Secure: %q", setCookie)
	}
	if len(token) != 43 {
		t.Errorf("token %q is not token_urlsafe(32)", token)
	}
	wantStatus(t, c.postForm("/chat", form), 403, "cookie without token")
	wantStatus(t, c.postForm("/chat", form, [2]string{CSRFHeader, "stale"}), 403, "stale header")
	wantStatus(t, c.postForm("/chat", form, [2]string{CSRFHeader, token}, [2]string{"Origin", "https://evil.test"}), 403, "cross origin")
	ok := c.postForm("/chat", form, [2]string{CSRFHeader, token}, [2]string{"Origin", "http://testserver"})
	wantStatus(t, ok, 303, "header token")
	if !strings.HasPrefix(ok.Header().Get("Location"), "/chat/") {
		t.Errorf("location = %q", ok.Header().Get("Location"))
	}
	wantStatus(t, c.postForm("/chat", url.Values{"text": {""}, CSRFField: {token}}), 303, "form field token")
	wantStatus(t, c.postForm("/chat", url.Values{"text": {""}, CSRFField: {"nope"}}), 403, "stale form field")
	wantStatus(t, c.postForm("/chat", url.Values{"text": {""}, CSRFField: {""}}), 403, "blank form field")
	if rec := c.postForm("/chat", form); rec.Body.String() != "Forbidden: missing or stale form token" {
		t.Errorf("body = %q", rec.Body.String())
	}
}

func TestCSRFFormFieldIsPercentDecoded(t *testing.T) {
	c := newClient(t, newServer(t, AuthLocal, []string{testHost}))
	c.get("/chat")
	body := CSRFField + "=" + strings.NewReplacer("-", "%2D", "_", "%5F").Replace(c.token())
	rec := c.do(http.MethodPost, "/chat", strings.NewReader("a=1&"+body), [2]string{"Content-Type", formType + "; charset=UTF-8"})
	wantStatus(t, rec, 303, "decoded token")
}

func TestCSRFCookieUnsafeMethodsAndDelete(t *testing.T) {
	c := local(t)
	wantStatus(t, c.do(http.MethodDelete, "/rules/shop", nil), 200, "DELETE with header")
	wantStatus(t, c.do(http.MethodDelete, "/rules/shop", nil, [2]string{CSRFHeader, "x"}), 403, "DELETE stale")
	for _, method := range []string{http.MethodPut, http.MethodPatch} {
		fresh := newClient(t, newServer(t, AuthLocal, []string{testHost}))
		wantStatus(t, fresh.do(method, "/chat", nil), 403, method+" without cookie")
	}
	wantStatus(t, c.get("/chat", [2]string{"Origin", "https://evil.test"}), 200, "GET ignores Origin")
}

func TestCSRFMultipartNeedsHeader(t *testing.T) {
	c := newClient(t, newServer(t, AuthLocal, []string{testHost}))
	c.get("/chat")
	body := "--b\r\nContent-Disposition: form-data; name=\"csrf_token\"\r\n\r\n" + c.token() + "\r\n--b--\r\n"
	mp := [2]string{"Content-Type", "multipart/form-data; boundary=b"}
	wantStatus(t, c.do(http.MethodPost, "/chat", strings.NewReader(body), mp), 403, "multipart field ignored")
	wantStatus(t, c.do(http.MethodPost, "/chat", strings.NewReader(body), mp, [2]string{CSRFHeader, c.token()}), 303, "multipart with header")
}

func TestCSRFCookieSecureFlag(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*http.Request)
		secure bool
	}{
		{"plain", func(*http.Request) {}, false},
		{"forwarded https", func(r *http.Request) { r.Header.Set("X-Forwarded-Proto", "https") }, true},
		{"forwarded http", func(r *http.Request) { r.Header.Set("X-Forwarded-Proto", "http") }, false},
		{"tls", func(r *http.Request) { r.TLS = &tlsState }, true},
	}
	srv := newServer(t, AuthLocal, []string{testHost})
	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodGet, "/chat", nil)
		req.Host = testHost
		tc.mutate(req)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if got := strings.Contains(rec.Header().Get("Set-Cookie"), "; Secure"); got != tc.secure {
			t.Errorf("%s: Secure = %v, want %v (%q)", tc.name, got, tc.secure, rec.Header().Get("Set-Cookie"))
		}
	}
}

func TestCSRFCookieIssuedOnlyWhenFresh(t *testing.T) {
	c := newClient(t, newServer(t, AuthLocal, []string{testHost}))
	if c.get("/chat").Header().Get("Set-Cookie") == "" {
		t.Fatal("no cookie issued")
	}
	if got := c.get("/chat").Header().Get("Set-Cookie"); got != "" {
		t.Errorf("cookie reissued: %q", got)
	}
	if !strings.Contains(c.get("/chat").Body.String(), c.token()) {
		t.Error("handler did not see the request's token")
	}
}

func TestSameOrigin(t *testing.T) {
	cases := []struct {
		origin, host string
		want         bool
	}{
		{"http://testserver", "testserver", true},
		{"https://TestServer", "testserver", true},
		{"http://testserver:8481", "testserver:8481", true},
		{"http://testserver:8481", "testserver", false},
		{"https://evil.test", "testserver", false},
		{"null", "testserver", false},
		{"", "testserver", false},
		{"testserver", "testserver", false},
		{"http://[::1]:8481", "[::1]:8481", true},
		{"http://[::1", "[::1", false},
		{"http://testserver/path?x#y", "testserver", true},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodPost, "/chat", nil)
		req.Host = tc.host
		req.Header.Set("Origin", tc.origin)
		if got := sameOrigin(req); got != tc.want {
			t.Errorf("Origin %q Host %q = %v, want %v", tc.origin, tc.host, got, tc.want)
		}
	}
	req := httptest.NewRequest(http.MethodPost, "/chat", nil)
	if !sameOrigin(req) {
		t.Error("absent Origin rejected")
	}
}

func TestBodyCap(t *testing.T) {
	c := newClient(t, newServer(t, AuthLocal, []string{testHost}))
	c.get("/chat")
	pad := func(n int) io.Reader {
		return io.MultiReader(strings.NewReader(CSRFField+"="+c.token()+"&pad="), strings.NewReader(strings.Repeat("x", n)))
	}
	form := [2]string{"Content-Type", formType}
	wantStatus(t, c.do(http.MethodPost, "/chat", pad(BodyCap), form), 413, "over the cap")
	if body := c.do(http.MethodPost, "/chat", pad(BodyCap), form).Body.String(); body != "Request too large" {
		t.Errorf("body = %q", body)
	}
	prefix := len(CSRFField + "=" + c.token() + "&pad=")
	wantStatus(t, c.do(http.MethodPost, "/chat", pad(BodyCap-prefix), form), 303, "exactly the cap")
	wantStatus(t, c.do(http.MethodPost, "/chat", pad(BodyCap), form, [2]string{CSRFHeader, c.token()}), 303, "header skips the sniff")
}

func TestBodyIsReplayedToTheHandler(t *testing.T) {
	var seen string
	mod := func(r chi.Router, _ *Deps) {
		r.Post("/echo", func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			seen = string(b)
		})
	}
	c := newClient(t, newServer(t, AuthLocal, []string{testHost}, func(o *Options) { o.Modules = []Module{mod} }))
	c.get("/echo")
	c.cookie = CSRFCookie + "=tok"
	body := "a=1&" + CSRFField + "=tok&b=2"
	wantStatus(t, c.do(http.MethodPost, "/echo", strings.NewReader(body), [2]string{"Content-Type", formType}), 200, "echo")
	if seen != body {
		t.Errorf("handler saw %q, want %q", seen, body)
	}
}

func TestSecurityHeaders(t *testing.T) {
	c := local(t)
	h := c.get("/chat").Header()
	csp := h.Get("Content-Security-Policy")
	for _, want := range []string{"default-src 'self'", "script-src 'self'", "img-src 'self' data:", "frame-ancestors 'none'", "form-action 'self'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP %q lacks %q", csp, want)
		}
	}
	if strings.Contains(csp, "script-src 'self' 'unsafe-inline'") || strings.Contains(csp, "unsafe-eval") {
		t.Errorf("CSP allows inline scripts: %q", csp)
	}
	if h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Referrer-Policy") != "same-origin" {
		t.Errorf("headers = %v", h)
	}
	denied := newClient(t, newServer(t, AuthProxy, []string{testHost})).get("/chat")
	if denied.Header().Get("Content-Security-Policy") != "" {
		t.Error("deny response carries security headers")
	}
}

func TestGzip(t *testing.T) {
	c := local(t)
	gz := [2]string{"Accept-Encoding", "gzip, deflate"}
	big := c.get("/big", gz)
	if big.Header().Get("Content-Encoding") != "gzip" || !strings.Contains(big.Header().Get("Vary"), "Accept-Encoding") {
		t.Fatalf("big headers = %v", big.Header())
	}
	r, err := gzip.NewReader(bytes.NewReader(big.Body.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	plain, _ := io.ReadAll(r)
	if string(plain) != strings.Repeat("a", 2000) {
		t.Errorf("decoded %d bytes", len(plain))
	}
	if small := c.get("/small", gz); small.Header().Get("Content-Encoding") != "" || small.Body.String() != "tiny" {
		t.Errorf("small response was compressed: %v", small.Header())
	}
	if none := c.get("/big"); none.Header().Get("Content-Encoding") != "" || none.Body.Len() != 2000 {
		t.Errorf("compressed without Accept-Encoding: %v", none.Header())
	}
}

func TestErrorHandling(t *testing.T) {
	c := local(t)
	missing := c.get("/nowhere")
	wantStatus(t, missing, 404, "unknown route")
	if missing.Body.String() != "Not Found." {
		t.Errorf("404 body = %q", missing.Body.String())
	}
	if got := c.get("/missing").Body.String(); got != "Unknown message." {
		t.Errorf("handler 404 body = %q", got)
	}
	conflict := c.get("/conflict")
	wantStatus(t, conflict, 409, "other status")
	if conflict.Body.String() != `{"detail":"subagent threads are read-only"}` || conflict.Header().Get("Content-Type") != "application/json" {
		t.Errorf("409 = %q %v", conflict.Body.String(), conflict.Header())
	}
	notAllowed := c.do(http.MethodGet, "/rules/x", nil)
	wantStatus(t, notAllowed, 405, "method not allowed")
	if notAllowed.Body.String() != `{"detail":"Method Not Allowed"}` {
		t.Errorf("405 body = %q", notAllowed.Body.String())
	}
	boom := c.get("/boom")
	wantStatus(t, boom, 500, "panic")
	if boom.Body.String() != "Internal Server Error" {
		t.Errorf("500 body = %q", boom.Body.String())
	}
	for _, docs := range []string{"/docs", "/redoc", "/openapi.json"} {
		wantStatus(t, c.get(docs), 404, docs)
	}
}

func TestErrorPageHookReceivesProblem(t *testing.T) {
	var gotStatus int
	var gotProblem string
	hook := func(o *Options) {
		o.ErrorPage = func(w http.ResponseWriter, _ *http.Request, status int, problem string) {
			gotStatus, gotProblem = status, problem
			w.WriteHeader(status)
		}
	}
	c := newClient(t, newServer(t, AuthLocal, []string{testHost}, hook))
	c.get("/nowhere")
	if gotStatus != 404 || gotProblem != "Not Found." {
		t.Errorf("hook saw %d %q", gotStatus, gotProblem)
	}
}

func TestRootRedirectsToChat(t *testing.T) {
	rec := local(t).get("/")
	wantStatus(t, rec, 303, "root")
	if rec.Header().Get("Location") != "/chat" {
		t.Errorf("location = %q", rec.Header().Get("Location"))
	}
}

func TestTrailingSlashRedirect(t *testing.T) {
	c := local(t)
	rec := c.get("/slash?x=1")
	wantStatus(t, rec, 307, "add slash")
	if rec.Header().Get("Location") != "http://testserver/slash/?x=1" {
		t.Errorf("location = %q", rec.Header().Get("Location"))
	}
	rec = c.get("/chat/")
	wantStatus(t, rec, 307, "drop slash")
	if rec.Header().Get("Location") != "http://testserver/chat" {
		t.Errorf("location = %q", rec.Header().Get("Location"))
	}
	wantStatus(t, c.get("/unrouted/"), 404, "unrouted")
}

func TestIsPartial(t *testing.T) {
	cases := []struct {
		headers map[string]string
		want    bool
	}{
		{nil, false},
		{map[string]string{"HX-Request": "true"}, true},
		{map[string]string{"HX-Request": "true", "HX-History-Restore-Request": "true"}, false},
		{map[string]string{"HX-History-Restore-Request": "true"}, false},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		for k, v := range tc.headers {
			req.Header.Set(k, v)
		}
		if got := IsPartial(req); got != tc.want {
			t.Errorf("%v = %v, want %v", tc.headers, got, tc.want)
		}
	}
}

func TestStaticServing(t *testing.T) {
	c := local(t)
	css := c.get("/static/app.css")
	wantStatus(t, css, 200, "app.css")
	if !strings.HasPrefix(css.Header().Get("Content-Type"), "text/css") || !strings.Contains(css.Body.String(), "--background") {
		t.Errorf("app.css = %v", css.Header())
	}
	if js := c.get("/static/basecoat/all.min.js"); js.Code != 200 || !strings.Contains(js.Header().Get("Content-Type"), "javascript") {
		t.Errorf("nested js = %d %v", js.Code, js.Header())
	}
	wantStatus(t, c.get("/static/missing.js"), 404, "missing")
	wantStatus(t, c.get("/static/basecoat"), 404, "directory")
	wantStatus(t, c.get("/static/"), 404, "root")
	wantStatus(t, c.get("/static/../static.go"), 404, "traversal")
	wantStatus(t, c.get("/static/%2e%2e/static.go"), 404, "encoded traversal")
	wantStatus(t, c.get("/static/app.css?v="+AssetVersion), 200, "versioned")
	post := c.do(http.MethodPost, "/static/app.css", nil)
	wantStatus(t, post, 405, "POST")
	head := c.do(http.MethodHead, "/static/app.css", nil)
	if head.Code != 200 || head.Body.Len() != 0 {
		t.Errorf("HEAD = %d body %d", head.Code, head.Body.Len())
	}
	etag := css.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag")
	}
	wantStatus(t, c.get("/static/app.css", [2]string{"If-None-Match", etag}), 304, "conditional")
	ranged := c.get("/static/app.css", [2]string{"Range", "bytes=0-9"})
	if ranged.Code != 206 || ranged.Body.Len() != 10 {
		t.Errorf("range = %d, %d bytes", ranged.Code, ranged.Body.Len())
	}
}

func TestStaticSlashRedirect(t *testing.T) {
	rec := local(t).get("/static")
	wantStatus(t, rec, 307, "/static")
	if rec.Header().Get("Location") != "http://testserver/static/" {
		t.Errorf("location = %q", rec.Header().Get("Location"))
	}
}

func TestFormFieldMatchesParseQS(t *testing.T) {
	cases := []struct {
		body, want string
		ok         bool
	}{
		{"csrf_token=abc", "abc", true},
		{"a=1&csrf_token=abc&csrf_token=def", "abc", true},
		{"csrf_token=", "", false},
		{"csrf_token", "", false},
		{"csrf%5Ftoken=a%2Bb+c", "a+b c", true},
		{"csrf_token=%zz", "%zz", true},
		{"csrf_token=%4", "%4", true},
		{"", "", false},
	}
	for _, tc := range cases {
		got, ok := formField(tc.body, CSRFField)
		if got != tc.want || ok != tc.ok {
			t.Errorf("formField(%q) = %q, %v; want %q, %v", tc.body, got, ok, tc.want, tc.ok)
		}
	}
}

func TestPanicWithGzipIs500WithoutGuardHeaders(t *testing.T) {
	c := newClient(t, newServer(t, AuthLocal, []string{testHost}))
	rec := c.get("/boom", [2]string{"Accept-Encoding", "gzip"})
	wantStatus(t, rec, 500, "panic with gzip")
	h := rec.Header()
	if h.Get("Content-Security-Policy") != "" || h.Get("Set-Cookie") != "" {
		t.Errorf("500 carries guard headers: %v", h)
	}
}

func TestCookieTokenIsLenient(t *testing.T) {
	cases := map[string]string{
		"atrium_csrf=tok;":                 "tok",
		"a:b=1; atrium_csrf=tok":           "tok",
		`prefs={"a":1}; atrium_csrf=tok`:   "tok",
		"lang=caf\u00e9; atrium_csrf=tok":  "tok",
		"atrium_csrf=old; atrium_csrf=new": "new",
		`atrium_csrf="quoted"`:             "quoted",
		"junk; atrium_csrf=tok; =x":        "tok",
	}
	for raw, want := range cases {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Cookie", raw)
		if got, ok := cookieToken(req); !ok || got != want {
			t.Errorf("%q = %q, %v; want %q", raw, got, ok, want)
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Cookie", "other=1")
	if _, ok := cookieToken(req); ok {
		t.Error("found a token in a foreign cookie")
	}
}

func TestHealthzExemptionNeedsExactPath(t *testing.T) {
	c := newClient(t, newServer(t, AuthProxy, []string{"atrium.test"}))
	c.host = "127.0.0.1:8480"
	wantStatus(t, c.get("/healthz"), 200, "exact")
	wantStatus(t, c.get("/health%7Az"), 400, "escaped variant goes through the Guard")
	wantStatus(t, c.get("/healthz/"), 400, "trailing slash")
}

func TestMethodErrors(t *testing.T) {
	c := local(t)
	post := c.do(http.MethodPost, "/static/app.css", nil)
	wantStatus(t, post, 405, "POST static")
	if post.Header().Get("Allow") != "" || post.Body.String() != `{"detail":"Method Not Allowed"}` {
		t.Errorf("static 405 = %v %q", post.Header(), post.Body.String())
	}
	rec := c.do(http.MethodGet, "/rules/x", nil)
	wantStatus(t, rec, 405, "route 405")
	if rec.Header().Get("Allow") != "DELETE" {
		t.Errorf("Allow = %q", rec.Header().Get("Allow"))
	}
	wantStatus(t, c.do(http.MethodHead, "/small", nil), 200, "HEAD follows GET")
	redirect := c.do(http.MethodPost, "/chat/", nil)
	wantStatus(t, redirect, 307, "method-differs slash redirect")
}

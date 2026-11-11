package main

import (
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/auth/oidc/oidctest"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
)

type wimSite struct {
	t    *testing.T
	base string
	idp  *oidctest.Provider
	db   *db.DB
}

type wimReply struct {
	status int
	header http.Header
	body   string
}

func (r wimReply) location() string { return r.header.Get("Location") }

type wimBrowser struct {
	site *wimSite
	jar  *cookiejar.Jar
	http *http.Client
}

func wimOIDCSite(t *testing.T) *wimSite {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(l.Addr().String())
	_ = l.Close()
	base := "http://127.0.0.1:" + port
	idp := oidctest.New(t, oidctest.Options{ClientSecret: "s3cret-for-the-test"})
	idp.AddUser(oidctest.User{Name: "admin", Email: "boss@example.com", Groups: []string{"atrium_access", "atrium_admin"}})
	idp.AddUser(oidctest.User{Name: "member", Email: "friend@example.com", Groups: []string{"atrium_access"}})
	idp.AddUser(oidctest.User{Name: "outsider", Email: "stranger@example.com", Groups: []string{"other"}})
	idp.AddUser(oidctest.User{Name: "boss", Email: "owner@example.com", Groups: []string{"team_access", "team_admin"}})
	dir := t.TempDir()
	setEnv(t, settingEnv(config.KeyPublicURL), base)
	setEnv(t, "ATRIUM_HOST_CONFIG", oidcHostConfig(t, dir, oidcHost{
		Issuer:      idp.Issuer(),
		ClientID:    idp.ClientID(),
		Secret:      "s3cret-for-the-test",
		GroupsClaim: "groups",
	}))
	got, _ := wimServeIn(t, dir, "--port", port, "--auth", "oidc", "--allowed-host", "127.0.0.1")
	if got != base {
		t.Fatalf("serve listens on %s, want %s", got, base)
	}
	d, err := db.Connect(filepath.Join(dir, "atrium.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return &wimSite{t: t, base: base, idp: idp, db: d}
}

func (s *wimSite) count(query string, args ...any) int {
	s.t.Helper()
	var n int
	if err := s.db.Read.QueryRow(query, args...).Scan(&n); err != nil {
		s.t.Fatal(err)
	}
	return n
}

func (s *wimSite) exec(query string, args ...any) {
	s.t.Helper()
	if _, err := s.db.Write.Exec(query, args...); err != nil {
		s.t.Fatal(err)
	}
}

func (s *wimSite) browser() *wimBrowser {
	jar, err := cookiejar.New(nil)
	if err != nil {
		s.t.Fatal(err)
	}
	return &wimBrowser{site: s, jar: jar, http: &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (b *wimBrowser) do(method, target string, form url.Values, headers ...string) wimReply {
	t := b.site.t
	t.Helper()
	if !strings.HasPrefix(target, "http") {
		target = b.site.base + target
	}
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequest(method, target, body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", "text/html")
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", b.site.base)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		if headers[i] == "Host" {
			req.Host = headers[i+1]
			continue
		}
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := b.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return wimReply{resp.StatusCode, resp.Header, string(data)}
}

func (b *wimBrowser) get(target string, headers ...string) wimReply {
	b.site.t.Helper()
	return b.do(http.MethodGet, target, nil, headers...)
}

func (b *wimBrowser) cookie(name string) string {
	u, _ := url.Parse(b.site.base)
	for _, c := range b.jar.Cookies(u) {
		if c.Name == name {
			return c.Value
		}
	}
	return ""
}

func (b *wimBrowser) post(target string, form url.Values, headers ...string) wimReply {
	b.site.t.Helper()
	if b.cookie("atrium_csrf") == "" {
		b.get("/login")
	}
	values := url.Values{"csrf_token": {b.cookie("atrium_csrf")}}
	for k, v := range form {
		values[k] = v
	}
	return b.do(http.MethodPost, target, values, headers...)
}

func (b *wimBrowser) signIn(user string, headers ...string) wimReply {
	t := b.site.t
	t.Helper()
	b.site.idp.Approve(user)
	start := b.post("/login", url.Values{"next": {""}}, headers...)
	if start.status != http.StatusSeeOther {
		t.Fatalf("POST /login: %d %.300s", start.status, start.body)
	}
	authz := b.get(start.location())
	if authz.status != http.StatusFound {
		t.Fatalf("authorize: %d %s", authz.status, authz.body)
	}
	return b.get(authz.location(), headers...)
}

func (b *wimBrowser) mustSignIn(user string) {
	b.site.t.Helper()
	if cb := b.signIn(user); cb.status != http.StatusSeeOther {
		b.site.t.Fatalf("callback for %s: %d %.300s", user, cb.status, cb.body)
	}
}

func wimExpect(t *testing.T, got wimReply, status int, contains ...string) {
	t.Helper()
	if got.status != status {
		t.Fatalf("status %d, want %d; location %q body %.300q", got.status, status, got.location(), got.body)
	}
	for _, want := range contains {
		if !strings.Contains(got.body, want) {
			t.Fatalf("body lacks %q: %.400q", want, got.body)
		}
	}
}

func TestWimOIDCSignInGrantsByGroupAndIgnoresForgedProxyHeaders(t *testing.T) {
	t.Parallel()
	s := wimOIDCSite(t)
	anon := s.browser()
	redirect := anon.get("/settings/auth")
	wimExpect(t, redirect, http.StatusSeeOther)
	if redirect.location() != "/login?next=%2Fsettings%2Fauth" {
		t.Fatalf("anonymous redirect %q", redirect.location())
	}
	hx := anon.get("/settings/auth", "HX-Request", "true")
	if hx.status != http.StatusUnauthorized || hx.header.Get("HX-Redirect") != "/login" {
		t.Fatalf("anonymous htmx request: %d %q", hx.status, hx.header.Get("HX-Redirect"))
	}
	forged := []string{"Remote-Groups", "atrium_access,atrium_admin", "Remote-User", "root"}
	wimExpect(t, anon.get("/settings/auth", forged...), http.StatusSeeOther)
	login := anon.get("/login")
	wimExpect(t, login, http.StatusOK, "<form")
	if csp := login.header.Get("Content-Security-Policy"); !strings.Contains(csp, "form-action 'self' "+strings.TrimSuffix(s.idp.Issuer(), "/")) {
		t.Fatalf("login CSP %q", csp)
	}
	if strings.Contains(anon.get("/login?reason=<script>").body, "<script>") {
		t.Fatal("login reason reflected")
	}
	wimExpect(t, anon.get("/login", "Host", "evil.example"), http.StatusBadRequest)
	wimExpect(t, anon.post("/login", nil, "Origin", "https://evil.example"), http.StatusForbidden)

	admin := s.browser()
	cb := admin.signIn("admin")
	wimExpect(t, cb, http.StatusSeeOther)
	if cb.location() != "/" {
		t.Fatalf("callback location %q", cb.location())
	}
	cookie := strings.Join(cb.header.Values("Set-Cookie"), "\n")
	for _, want := range []string{"atrium_session=", "HttpOnly", "SameSite=Lax", "Path=/"} {
		if !strings.Contains(cookie, want) {
			t.Fatalf("session cookie lacks %q: %s", want, cookie)
		}
	}
	wimExpect(t, admin.get("/settings/auth"), http.StatusOK, "Sign-in")
	wimExpect(t, admin.get("/login"), http.StatusSeeOther)
	for _, a := range s.idp.Authorizations() {
		if a.Get("code_challenge_method") != "S256" || a.Get("code_challenge") == "" || a.Get("state") == "" || a.Get("nonce") == "" || a.Get("redirect_uri") != s.base+"/auth/callback" {
			t.Fatalf("authorization request %v", a)
		}
	}

	member := s.browser()
	member.mustSignIn("member")
	wimExpect(t, member.get("/settings/auth"), http.StatusForbidden, "Admins only", `action="/logout"`)
	wimExpect(t, member.get("/settings/auth", forged...), http.StatusForbidden)

	outsider := s.browser()
	wimExpect(t, outsider.signIn("outsider"), http.StatusForbidden, "This account does not have access.")
	wimExpect(t, outsider.get("/settings/auth"), http.StatusSeeOther)
	if n := s.count("SELECT COUNT(*) FROM auth_sessions"); n != 2 {
		t.Fatalf("%d sessions", n)
	}
	if n := s.count("SELECT COUNT(*) FROM auth_sessions WHERE access_token NOT LIKE 'v1:%' OR refresh_token NOT LIKE 'v1:%'"); n != 0 {
		t.Fatalf("%d sessions hold unsealed provider tokens", n)
	}

	secure := s.browser()
	https := []string{"X-Forwarded-Proto", "https"}
	s.idp.Approve("admin")
	start := secure.post("/login", url.Values{}, https...)
	authz := secure.get(start.location())
	hosted := strings.Join(secure.get(authz.location(), https...).header.Values("Set-Cookie"), "\n")
	if !strings.Contains(hosted, "__Host-atrium_session=") || !strings.Contains(hosted, "Secure") || strings.Contains(hosted, "Domain=") {
		t.Fatalf("https session cookie: %s", hosted)
	}
}

func TestWimOIDCRefusesReplayedForgedAndTamperedSignIns(t *testing.T) {
	t.Parallel()
	s := wimOIDCSite(t)
	b := s.browser()
	s.idp.Approve("admin")
	start := b.post("/login", url.Values{"next": {"/settings/auth"}})
	callback := b.get(start.location()).location()

	other := s.browser()
	other.get("/login")
	wimExpect(t, other.get(callback), http.StatusBadRequest, "The provider rejected the sign-in.")
	wimExpect(t, b.get(callback), http.StatusSeeOther)
	wimExpect(t, b.get(callback), http.StatusBadRequest)

	again := b.post("/login", url.Values{})
	u, _ := url.Parse(b.get(again.location()).location())
	q := u.Query()
	q.Set("state", "forged")
	u.RawQuery = q.Encode()
	wimExpect(t, b.get(u.String()), http.StatusBadRequest)
	wimExpect(t, b.get("/auth/callback?error=access_denied&state=x"), http.StatusBadRequest)

	late := s.browser()
	s.idp.Approve("admin")
	lateStart := late.post("/login", url.Values{})
	lateCallback := late.get(lateStart.location()).location()
	s.exec("UPDATE auth_logins SET created_at = created_at - 3600")
	wimExpect(t, late.get(lateCallback), http.StatusBadRequest, "Sign-in timed out.")

	for name, tamper := range map[string]func(*oidctest.Provider){
		"wrong audience": func(p *oidctest.Provider) { p.Tamper = func(c map[string]any) { c["aud"] = "someone-else" } },
		"wrong nonce":    func(p *oidctest.Provider) { p.Tamper = func(c map[string]any) { c["nonce"] = "replayed" } },
		"wrong issuer":   func(p *oidctest.Provider) { p.Tamper = func(c map[string]any) { c["iss"] = "https://evil.example" } },
		"expired":        func(p *oidctest.Provider) { p.Expired = true },
		"alg none":       func(p *oidctest.Provider) { p.Forge = "none" },
		"bad signature":  func(p *oidctest.Provider) { p.Forge = "badsig" },
	} {
		tamper(s.idp)
		victim := s.browser()
		got := victim.signIn("admin")
		s.idp.Tamper, s.idp.Expired, s.idp.Forge = nil, false, ""
		if got.status != http.StatusBadRequest || !strings.Contains(got.body, "The provider rejected the sign-in.") {
			t.Errorf("%s: %d %.200q", name, got.status, got.body)
		}
		if r := victim.get("/settings/auth"); r.status != http.StatusSeeOther {
			t.Errorf("%s: a tampered sign-in opened a session: %d", name, r.status)
		}
	}
	if n := s.count("SELECT COUNT(*) FROM auth_sessions"); n != 1 {
		t.Fatalf("%d sessions", n)
	}

	s.browser().mustSignIn("admin")
}

func TestWimOIDCSessionsEndOnRevocationExpiryAndSignOut(t *testing.T) {
	t.Parallel()
	s := wimOIDCSite(t)
	stale := "UPDATE auth_sessions SET rechecked_at = rechecked_at - 7200 WHERE subject = ?"

	b := s.browser()
	b.mustSignIn("admin")
	s.exec(stale, "sub-admin")
	wimExpect(t, b.get("/settings/auth"), http.StatusOK)
	if s.idp.Refreshes() != 1 {
		t.Fatalf("%d refresh calls", s.idp.Refreshes())
	}
	wimExpect(t, b.get("/settings/auth"), http.StatusOK)
	if s.idp.Refreshes() != 1 {
		t.Fatalf("re-checked again within the hour: %d", s.idp.Refreshes())
	}
	s.idp.SetGroups("admin", "atrium_access")
	s.exec(stale, "sub-admin")
	wimExpect(t, b.get("/settings/auth"), http.StatusForbidden)
	s.idp.SetGroups("admin", "other")
	s.exec(stale, "sub-admin")
	gone := b.get("/settings/auth")
	if gone.status != http.StatusSeeOther || !strings.Contains(gone.location(), "reason=revoked") {
		t.Fatalf("revoked session: %d %q", gone.status, gone.location())
	}
	wimExpect(t, b.get("/login?reason=revoked"), http.StatusOK, "Your access was removed.")

	refused := s.browser()
	refused.mustSignIn("member")
	s.idp.RevokeAll = true
	s.exec(stale, "sub-member")
	wimExpect(t, refused.get("/settings/auth"), http.StatusSeeOther)
	s.idp.RevokeAll = false
	if n := s.count("SELECT COUNT(*) FROM auth_sessions"); n != 0 {
		t.Fatalf("%d sessions after revocation", n)
	}

	for _, column := range []string{"idle_expires_at", "absolute_expires_at"} {
		e := s.browser()
		e.mustSignIn("member")
		s.exec("UPDATE auth_sessions SET " + column + " = 1")
		out := e.get("/settings/auth")
		cleared := false
		for _, c := range out.header.Values("Set-Cookie") {
			cleared = cleared || (strings.HasPrefix(c, "atrium_session=;") && strings.Contains(c, "Max-Age=0"))
		}
		if out.status != http.StatusSeeOther || !strings.Contains(out.location(), "reason=expired") || !cleared {
			t.Fatalf("%s expiry: %d %q %v", column, out.status, out.location(), out.header.Values("Set-Cookie"))
		}
	}

	o := s.browser()
	o.mustSignIn("member")
	session := o.cookie("atrium_session")
	wimExpect(t, o.do(http.MethodPost, "/logout", url.Values{"csrf_token": {"wrong"}}), http.StatusForbidden)
	wimExpect(t, o.get("/logout"), http.StatusMethodNotAllowed)
	out := o.post("/logout", nil)
	if out.status != http.StatusSeeOther || out.location() != "/login?reason=signed_out" {
		t.Fatalf("logout: %d %q", out.status, out.location())
	}
	wimExpect(t, o.get("/settings/auth"), http.StatusSeeOther)
	wimExpect(t, s.browser().get("/settings/auth", "Cookie", "atrium_session="+session), http.StatusSeeOther)
	wimExpect(t, o.get(out.location()), http.StatusOK, "You are signed out.")
	if n := s.count("SELECT COUNT(*) FROM auth_sessions"); n != 0 {
		t.Fatalf("%d sessions after sign-out", n)
	}
}

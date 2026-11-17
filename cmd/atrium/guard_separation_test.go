package main

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

var crossSiteNavigation = []string{"Sec-Fetch-Site", "cross-site", "Sec-Fetch-Mode", "navigate", "Sec-Fetch-Dest", "document"}

func setCookieLine(t *testing.T, header http.Header, name string) string {
	t.Helper()
	for _, line := range header.Values("Set-Cookie") {
		if strings.HasPrefix(line, name+"=") {
			return line
		}
	}
	t.Fatalf("no Set-Cookie for %s in %q", name, header.Values("Set-Cookie"))
	return ""
}

func expectCookieAttributes(t *testing.T, line string, want ...string) {
	t.Helper()
	for _, attribute := range want {
		if !strings.Contains(line, attribute) {
			t.Fatalf("cookie %q lacks %q", line, attribute)
		}
	}
}

func TestGuardOIDCSessionCookieIsStrictWhileTheSignInBindingStaysLax(t *testing.T) {
	t.Parallel()
	s := wimOIDCSite(t)
	anon := s.browser()
	expectCookieAttributes(t, setCookieLine(t, anon.get("/login").header, "atrium_csrf"), "HttpOnly", "SameSite=Strict", "Path=/")

	b := s.browser()
	s.idp.Approve("admin")
	start := b.post("/login", url.Values{"next": {"/settings/auth"}})
	wimExpect(t, start, http.StatusSeeOther)
	expectCookieAttributes(t, setCookieLine(t, start.header, "atrium_oidc_state"), "HttpOnly", "SameSite=Lax", "Path=/", "Max-Age=600")
	authz := b.get(start.location())
	wimExpect(t, authz, http.StatusFound)
	cb := b.get(authz.location())
	wimExpect(t, cb, http.StatusSeeOther)
	if cb.location() != "/settings/auth" {
		t.Fatalf("callback returns to %q, want /settings/auth", cb.location())
	}
	expectCookieAttributes(t, setCookieLine(t, cb.header, "atrium_session"), "HttpOnly", "SameSite=Strict", "Path=/")
	wimExpect(t, b.get("/settings/auth"), http.StatusOK, "Sign-in")
}

func TestGuardOIDCCrossSiteArrivalGetsOneSameSiteContinue(t *testing.T) {
	t.Parallel()
	s := wimOIDCSite(t)
	anon := s.browser()
	const target = "/settings/auth?tab=a&view=b"

	arrival := anon.get(target, crossSiteNavigation...)
	wimExpect(t, arrival, http.StatusOK, `content="0;url=/settings/auth?tab=a&amp;view=b"`, `<a href="/settings/auth?tab=a&amp;view=b">Continue to Atrium</a>`)
	if got := arrival.header.Values("Set-Cookie"); len(got) != 0 {
		t.Fatalf("the continue page sets cookies: %q", got)
	}
	if arrival.header.Get("Cache-Control") != "no-store" || !strings.Contains(arrival.header.Get("Content-Security-Policy"), "script-src 'self'") {
		t.Fatalf("continue page headers: %v", arrival.header)
	}
	if head := anon.do(http.MethodHead, target, nil, crossSiteNavigation...); head.status != http.StatusOK || head.body != "" {
		t.Fatalf("HEAD cross-site arrival: %d %q", head.status, head.body)
	}

	followed := anon.get(target, "Sec-Fetch-Site", "same-origin", "Sec-Fetch-Mode", "navigate", "Sec-Fetch-Dest", "document")
	wimExpect(t, followed, http.StatusSeeOther)
	if followed.location() != "/login?next=%2Fsettings%2Fauth%3Ftab%3Da%26view%3Db" {
		t.Fatalf("a same-origin arrival without a session goes to %q", followed.location())
	}
	if unmarked := anon.get(target); unmarked.status != http.StatusSeeOther || !strings.HasPrefix(unmarked.location(), "/login?next=") {
		t.Fatalf("an arrival without fetch metadata: %d %q", unmarked.status, unmarked.location())
	}
	hx := anon.get(target, append([]string{"HX-Request", "true"}, crossSiteNavigation...)...)
	if hx.status != http.StatusUnauthorized || hx.header.Get("HX-Redirect") != "/login" {
		t.Fatalf("cross-site partial: %d %q", hx.status, hx.header.Get("HX-Redirect"))
	}

	signedIn := s.browser()
	signedIn.mustSignIn("admin")
	wimExpect(t, signedIn.get("/settings/auth", crossSiteNavigation...), http.StatusOK, "Sign-in")
}

func TestGuardSurfacesTakeOnlyTheirOwnCredentials(t *testing.T) {
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	port, flags := handOverEmergency(t)
	s := startAPISite(t, dir, api, flags...)
	owner := s.token("owner")

	proxy := newWebClient(t, s.base, proxyOwner{http.DefaultTransport})
	if me := proxy.do(http.MethodGet, "/web/v1/me", nil); me.status != http.StatusOK {
		t.Fatalf("proxy identity on /web/v1: %d %s", me.status, me.body)
	}
	expectAPI(t, proxy.do(http.MethodGet, "/api/v1/me", nil), http.StatusUnauthorized, "unauthenticated")
	expectAPI(t, proxy.do(http.MethodGet, "/web/v1/me", nil, "Authorization", "Bearer "+owner), http.StatusBadRequest, "bearer_refused")

	session := newWebClient(t, s.base, nil)
	session.http = oidcBrowser(t, s.base, s.idp, "owner")
	if me := session.do(http.MethodGet, "/web/v1/me", nil); me.status != http.StatusOK {
		t.Fatalf("OIDC session on /web/v1: %d %s", me.status, me.body)
	}
	expectAPI(t, session.do(http.MethodGet, "/api/v1/me", nil), http.StatusUnauthorized, "unauthenticated")
	expectAPI(t, session.do(http.MethodGet, "/web/v1/me", nil, "Authorization", "Bearer "+owner), http.StatusBadRequest, "bearer_refused")

	local := newWebClient(t, "http://127.0.0.1:"+port, nil)
	if me := local.do(http.MethodGet, "/web/v1/me", nil); me.status != http.StatusOK {
		t.Fatalf("local identity on the loopback emergency listener: %d %s", me.status, me.body)
	}
	expectAPI(t, local.do(http.MethodGet, "/api/v1/me", nil), http.StatusUnauthorized, "unauthenticated")
	expectAPI(t, local.do(http.MethodGet, "/web/v1/me", nil, "Authorization", "Bearer "+owner), http.StatusBadRequest, "bearer_refused")

	if me := s.do(http.MethodGet, "/api/v1/me", owner, nil); me.status != http.StatusOK {
		t.Fatalf("the bearer on /api/v1: %d %s", me.status, me.body)
	}

	anon := newWebClient(t, s.base, nil)
	for _, h := range [][]string{
		{"X-Forwarded-For", "127.0.0.1"},
		{"X-Real-IP", "127.0.0.1"},
		{"Forwarded", "for=127.0.0.1"},
		{"Remote-User", "owner"},
		{"Remote-Groups", "atrium_admin"},
		{"Remote-Groups", "atrium_access_x"},
		{"Cookie", "atrium_session=forged"},
	} {
		got := anon.do(http.MethodGet, "/web/v1/me", nil, h...)
		if got.status != http.StatusUnauthorized {
			t.Fatalf("public listener with %s: %d %s", h[0], got.status, got.body)
		}
	}
	if host := anon.do(http.MethodGet, "/web/v1/me", nil, "Host", "localhost"); host.status != http.StatusBadRequest {
		t.Fatalf("public listener with Host localhost: %d %s", host.status, host.body)
	}
}

func TestGuardProxyHeadersNeedATrustedPeer(t *testing.T) {
	t.Parallel()
	base, _ := wimServeIn(t, t.TempDir(), "--auth", "proxy", "--allowed-host", "127.0.0.1", "--trusted-proxy", "192.0.2.0/24")
	headers := []string{"Remote-Groups", "atrium_access, atrium_admin", "Remote-User", "owner", "X-Forwarded-For", "192.0.2.7"}
	if status, _, _ := wimGet(t, base+"/settings/mail", headers...); status != http.StatusForbidden {
		t.Fatalf("an untrusted peer with proxy headers opened a page: %d", status)
	}
	if status, body, _ := wimGet(t, base+"/web/v1/me", append(headers, "Atrium-Client", "web")...); status != http.StatusUnauthorized {
		t.Fatalf("an untrusted peer with proxy headers on /web/v1: %d %s", status, body)
	}
}

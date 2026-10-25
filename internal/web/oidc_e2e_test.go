package web_test

import (
	"bytes"
	"context"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/auth/oidc"
	"github.com/jeiang/atrium/internal/auth/oidc/oidctest"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/testutil/golden"
	"github.com/jeiang/atrium/internal/web"
)

type shape struct {
	name     string
	preset   string
	idp      oidctest.Options
	settings map[string]string
	access   string
	admin    string
	emails   bool
}

var shapes = []shape{
	{name: "generic", preset: "generic", access: "atrium_access", admin: "atrium_admin"},
	{name: "pocket id", preset: "pocket_id", access: "atrium_access", admin: "atrium_admin"},
	{name: "authelia", preset: "authelia", idp: oidctest.Options{GroupsIn: "userinfo"}, access: "atrium_access", admin: "atrium_admin"},
	{name: "authentik", preset: "authentik", idp: oidctest.Options{IssuerPath: "/application/o/atrium/"}, access: "atrium_access", admin: "atrium_admin"},
	{name: "keycloak", preset: "keycloak", idp: oidctest.Options{IssuerPath: "/realms/atrium", GroupPrefix: "/"}, access: "atrium_access", admin: "atrium_admin"},
	{
		name: "entra", preset: "entra", idp: oidctest.Options{IssuerPath: "/0b2f6d4e/v2.0", AuthPost: true},
		settings: map[string]string{"oidc_access_group": "7f1c-access", "oidc_admin_group": "7f1c-admin"}, access: "7f1c-access", admin: "7f1c-admin",
	},
	{
		name: "google", preset: "google", emails: true,
		settings: map[string]string{"oidc_access_emails": "friend@example.com", "oidc_admin_emails": "boss@example.com"},
	},
	{name: "generic nested claim", preset: "generic", idp: oidctest.Options{GroupsClaim: "realm_access.roles"}, settings: map[string]string{"oidc_groups_claim": "realm_access.roles"}, access: "atrium_access", admin: "atrium_admin"},
}

func (s shape) users(r *rig) {
	if s.emails {
		r.idp.AddUser(oidctest.User{Name: "admin", Email: "boss@example.com", EmailVerified: true})
		r.idp.AddUser(oidctest.User{Name: "member", Email: "friend@example.com", EmailVerified: true})
		r.idp.AddUser(oidctest.User{Name: "outsider", Email: "stranger@example.com", EmailVerified: true})
		r.idp.AddUser(oidctest.User{Name: "unverified", Email: "boss@example.com", EmailVerified: false})
		return
	}
	r.idp.AddUser(oidctest.User{Name: "admin", Email: "boss@example.com", Groups: []string{s.access, s.admin}})
	r.idp.AddUser(oidctest.User{Name: "member", Email: "friend@example.com", Groups: []string{s.access}})
	r.idp.AddUser(oidctest.User{Name: "outsider", Email: "stranger@example.com", Groups: []string{"other"}})
}

func (s shape) rig(t *testing.T) *rig {
	r := newRig(t, rigOpts{preset: s.preset, idp: s.idp, settings: s.settings})
	s.users(r)
	return r
}

func TestOIDCSignInPerPresetShape(t *testing.T) {
	for _, s := range shapes {
		t.Run(s.name, func(t *testing.T) {
			r := s.rig(t)
			anon := r.browser()
			redirect := anon.get("/ping")
			expect(t, redirect, http.StatusSeeOther)
			if got := redirect.location(); got != "/login?next=%2Fping" {
				t.Fatalf("redirect %q", got)
			}
			admin := r.browser()
			cb := admin.signIn(r, "admin")
			expect(t, cb, http.StatusSeeOther)
			if cb.location() != "/" {
				t.Fatalf("callback location %q", cb.location())
			}
			expect(t, admin.get("/ping"), http.StatusOK, "source=oidc", "admin=yes")
			expect(t, admin.get("/settings/ping"), http.StatusOK, "admin=yes")
			expect(t, admin.post("/settings/ping", nil), http.StatusOK)
			chat := admin.get("/login")
			expect(t, chat, http.StatusSeeOther)

			member := r.browser()
			member.mustSignIn(r, "member")
			expect(t, member.get("/ping"), http.StatusOK, "admin=no")
			expect(t, member.get("/settings/ping"), http.StatusForbidden, "Admins only")
			expect(t, member.post("/settings/ping", nil), http.StatusForbidden)
			expect(t, member.post("/live/resume", nil), http.StatusForbidden)

			outsider := r.browser()
			expect(t, outsider.signIn(r, "outsider"), http.StatusForbidden, "This account does not have access.")
			expect(t, outsider.get("/ping"), http.StatusSeeOther)
			if n := r.count("SELECT COUNT(*) FROM auth_sessions"); n != 2 {
				t.Fatalf("%d sessions", n)
			}
			if s.emails {
				unverified := r.browser()
				expect(t, unverified.signIn(r, "unverified"), http.StatusForbidden)
			}
			for _, a := range r.idp.Authorizations() {
				if a.Get("code_challenge_method") != "S256" || a.Get("code_challenge") == "" || a.Get("state") == "" || a.Get("nonce") == "" {
					t.Fatalf("authorization request %v", a)
				}
				if a.Get("redirect_uri") != r.url+"/auth/callback" || a.Get("response_type") != "code" {
					t.Fatalf("authorization request %v", a)
				}
			}
		})
	}
}

func TestOIDCPresetRequestShape(t *testing.T) {
	google := shapes[6].rig(t)
	google.browser().mustSignIn(google, "admin")
	if got := google.idp.Authorizations()[0].Get("access_type"); got != "offline" {
		t.Fatalf("google access_type %q", got)
	}
	keycloak := shapes[4].rig(t)
	keycloak.browser().mustSignIn(keycloak, "member")
	if got := keycloak.idp.Authorizations()[0].Get("scope"); !strings.Contains(got, "offline_access") || strings.Contains(got, "groups") {
		t.Fatalf("keycloak scope %q", got)
	}
	entra := shapes[5].rig(t)
	entra.browser().mustSignIn(entra, "member")
	call := entra.idp.TokenCalls()[0]
	if call.Get("client_secret") != "s3cret" || call.Get("code_verifier") == "" {
		t.Fatalf("client_secret_post token call %v", call)
	}
	pocket := shapes[1].rig(t)
	pocket.browser().mustSignIn(pocket, "member")
	if call := pocket.idp.TokenCalls()[0]; call.Get("client_secret") != "" || call.Get("code_verifier") == "" {
		t.Fatalf("client_secret_basic token call %v", call)
	}
}

func TestOIDCRefusesProviderWithoutS256(t *testing.T) {
	r := newRig(t, rigOpts{preset: "generic", idp: oidctest.Options{NoS256: true}})
	r.idp.AddUser(oidctest.User{Name: "admin", Groups: []string{"atrium_admin"}})
	b := r.browser()
	r.idp.Approve("admin")
	expect(t, b.startLogin(""), http.StatusBadRequest, "The provider rejected the sign-in.")
	if n := len(r.idp.Authorizations()); n != 0 {
		t.Fatalf("authorization requests sent to a provider without S256: %d", n)
	}
}

func TestOIDCLoginStateIsBoundAndSingleUse(t *testing.T) {
	r := shapes[0].rig(t)
	b := r.browser()
	r.idp.Approve("admin")
	start := b.startLogin("/ping")
	authz := b.get(start.location())
	callback := authz.location()

	other := r.browser()
	other.get("/login")
	expect(t, other.get(callback), http.StatusBadRequest, "The provider rejected the sign-in.")

	expect(t, b.get(callback), http.StatusSeeOther)
	if got := b.get(callback); got.Status != http.StatusBadRequest {
		t.Fatalf("replayed callback %d", got.Status)
	}

	again := b.startLogin("")
	authz = b.get(again.location())
	u, _ := url.Parse(authz.location())
	q := u.Query()
	q.Set("state", "forged")
	u.RawQuery = q.Encode()
	expect(t, b.get(u.String()), http.StatusBadRequest)
	expect(t, b.get("/auth/callback"), http.StatusBadRequest)
	expect(t, b.get("/auth/callback?error=access_denied&state=x"), http.StatusBadRequest)
	if n := r.count("SELECT COUNT(*) FROM auth_sessions"); n != 1 {
		t.Fatalf("%d sessions", n)
	}
}

func TestOIDCLoginStateExpires(t *testing.T) {
	r := shapes[0].rig(t)
	b := r.browser()
	r.idp.Approve("admin")
	start := b.startLogin("")
	authz := b.get(start.location())
	r.exec("UPDATE auth_logins SET created_at = created_at - 3600")
	expect(t, b.get(authz.location()), http.StatusBadRequest, "Sign-in timed out.")
	if n := r.count("SELECT COUNT(*) FROM auth_sessions"); n != 0 {
		t.Fatalf("%d sessions", n)
	}
}

func TestOIDCRefusesBadIDTokens(t *testing.T) {
	cases := map[string]func(*oidctest.Provider){
		"wrong issuer":   func(p *oidctest.Provider) { p.Tamper = func(c map[string]any) { c["iss"] = "https://evil.example" } },
		"wrong audience": func(p *oidctest.Provider) { p.Tamper = func(c map[string]any) { c["aud"] = "someone-else" } },
		"wrong nonce":    func(p *oidctest.Provider) { p.Tamper = func(c map[string]any) { c["nonce"] = "replayed" } },
		"no subject":     func(p *oidctest.Provider) { p.Tamper = func(c map[string]any) { delete(c, "sub") } },
		"no expiry":      func(p *oidctest.Provider) { p.Tamper = func(c map[string]any) { delete(c, "exp") } },
		"expired":        func(p *oidctest.Provider) { p.Expired = true },
		"alg none":       func(p *oidctest.Provider) { p.Forge = "none" },
		"alg hs256":      func(p *oidctest.Provider) { p.Forge = "hs256" },
		"bad signature":  func(p *oidctest.Provider) { p.Forge = "badsig" },
		"two audiences without azp": func(p *oidctest.Provider) {
			p.Tamper = func(c map[string]any) { c["aud"] = []string{"atrium", "other"} }
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			r := shapes[0].rig(t)
			mutate(r.idp)
			b := r.browser()
			expect(t, b.signIn(r, "admin"), http.StatusBadRequest, "The provider rejected the sign-in.")
			if n := r.count("SELECT COUNT(*) FROM auth_sessions"); n != 0 {
				t.Fatalf("%d sessions", n)
			}
			expect(t, b.get("/ping"), http.StatusSeeOther)
		})
	}
}

func TestOIDCFollowsKeyRotation(t *testing.T) {
	r := shapes[0].rig(t)
	r.browser().mustSignIn(r, "admin")
	r.idp.RotateKey(true)
	r.browser().mustSignIn(r, "member")
	r.idp.RotateKey(false)
	r.browser().mustSignIn(r, "member")
}

func TestOIDCAcceptsEllipticKeys(t *testing.T) {
	r := newRig(t, rigOpts{preset: "generic", idp: oidctest.Options{Alg: "ES256"}})
	r.idp.AddUser(oidctest.User{Name: "admin", Groups: []string{"atrium_admin"}})
	b := r.browser()
	b.mustSignIn(r, "admin")
	expect(t, b.get("/settings/ping"), http.StatusOK)
}

func TestOIDCHourlyRecheck(t *testing.T) {
	for _, s := range []shape{shapes[0], shapes[2]} {
		t.Run(s.name, func(t *testing.T) {
			stale := "UPDATE auth_sessions SET rechecked_at = rechecked_at - 7200"
			r := s.rig(t)
			b := r.browser()
			b.mustSignIn(r, "admin")
			r.exec("UPDATE auth_sessions SET last_seen_at = last_seen_at - 3600, idle_expires_at = idle_expires_at - 6 * 86400")
			var before float64
			if err := r.db.Read.QueryRow("SELECT idle_expires_at FROM auth_sessions").Scan(&before); err != nil {
				t.Fatal(err)
			}
			r.exec(stale)
			expect(t, b.get("/ping"), http.StatusOK, "admin=yes")
			var after float64
			if err := r.db.Read.QueryRow("SELECT idle_expires_at FROM auth_sessions").Scan(&after); err != nil {
				t.Fatal(err)
			}
			if after-before < 5*86400 {
				t.Fatalf("idle expiry slid from %f to %f", before, after)
			}
			if r.idp.RefreshCall != 1 {
				t.Fatalf("%d refresh calls", r.idp.RefreshCall)
			}
			expect(t, b.get("/ping"), http.StatusOK)
			if r.idp.RefreshCall != 1 {
				t.Fatalf("re-checked again within the hour: %d", r.idp.RefreshCall)
			}

			r.idp.SetGroups("admin", s.access)
			r.exec(stale)
			expect(t, b.get("/settings/ping"), http.StatusForbidden)
			expect(t, b.get("/ping"), http.StatusOK, "admin=no")

			r.idp.SetGroups("admin", "other")
			r.exec(stale)
			gone := b.get("/ping")
			expect(t, gone, http.StatusSeeOther)
			if got := gone.location(); got != "/login?next=%2Fping&reason=revoked" {
				t.Fatalf("redirect %q", got)
			}
			if n := r.count("SELECT COUNT(*) FROM auth_sessions"); n != 0 {
				t.Fatalf("%d sessions after revocation", n)
			}
			expect(t, b.get("/login?reason=revoked"), http.StatusOK, "Your access was removed.")
		})
	}
}

func TestOIDCRecheckWhenProviderRejectsRefresh(t *testing.T) {
	r := shapes[0].rig(t)
	b := r.browser()
	b.mustSignIn(r, "admin")
	r.idp.RevokeAll = true
	r.exec("UPDATE auth_sessions SET rechecked_at = rechecked_at - 7200")
	expect(t, b.get("/ping"), http.StatusSeeOther)
	if n := r.count("SELECT COUNT(*) FROM auth_sessions"); n != 0 {
		t.Fatalf("%d sessions", n)
	}
}

func TestOIDCRecheckSurvivesBriefProviderOutage(t *testing.T) {
	r := shapes[0].rig(t)
	b := r.browser()
	b.mustSignIn(r, "admin")
	r.idp.Down = true
	r.exec("UPDATE auth_sessions SET rechecked_at = rechecked_at - 3700")
	expect(t, b.get("/ping"), http.StatusOK, "admin=yes")
	expect(t, b.get("/ping"), http.StatusOK, "admin=yes")
	r.exec("UPDATE auth_sessions SET rechecked_at = rechecked_at - 4000")
	expect(t, b.get("/ping"), http.StatusSeeOther)
	if n := r.count("SELECT COUNT(*) FROM auth_sessions"); n != 0 {
		t.Fatalf("%d sessions", n)
	}
}

func TestOIDCSessionExpiry(t *testing.T) {
	for name, column := range map[string]string{"idle": "idle_expires_at", "absolute": "absolute_expires_at"} {
		t.Run(name, func(t *testing.T) {
			r := shapes[0].rig(t)
			b := r.browser()
			b.mustSignIn(r, "admin")
			expect(t, b.get("/ping"), http.StatusOK)
			r.exec("UPDATE auth_sessions SET " + column + " = 1")
			gone := b.get("/ping")
			expect(t, gone, http.StatusSeeOther)
			if got := gone.location(); got != "/login?next=%2Fping&reason=expired" {
				t.Fatalf("redirect %q", got)
			}
			var cleared bool
			for _, c := range gone.Header.Values("Set-Cookie") {
				if strings.HasPrefix(c, "atrium_session=;") && strings.Contains(c, "Max-Age=0") {
					cleared = true
				}
			}
			if !cleared {
				t.Fatalf("session cookie not cleared: %v", gone.Header.Values("Set-Cookie"))
			}
			if n := r.count("SELECT COUNT(*) FROM auth_sessions"); n != 0 {
				t.Fatalf("%d sessions", n)
			}
		})
	}
}

func TestOIDCSessionLifetimeFollowsSettings(t *testing.T) {
	r := newRig(t, rigOpts{preset: "generic", settings: map[string]string{"oidc_session_idle_days": "2", "oidc_session_max_days": "3"}})
	r.idp.AddUser(oidctest.User{Name: "admin", Groups: []string{"atrium_admin"}})
	r.browser().mustSignIn(r, "admin")
	var created, idle, absolute float64
	if err := r.db.Read.QueryRow("SELECT created_at, idle_expires_at, absolute_expires_at FROM auth_sessions").Scan(&created, &idle, &absolute); err != nil {
		t.Fatal(err)
	}
	if d := idle - created; d < 2*86400-5 || d > 2*86400+5 {
		t.Fatalf("idle %f", d)
	}
	if d := absolute - created; d < 3*86400-5 || d > 3*86400+5 {
		t.Fatalf("absolute %f", d)
	}
}

func TestOIDCSignOut(t *testing.T) {
	r := shapes[0].rig(t)
	b := r.browser()
	b.mustSignIn(r, "admin")
	var cookie string
	u, _ := url.Parse(r.url)
	for _, c := range b.jar.Cookies(u) {
		if c.Name == "atrium_session" {
			cookie = c.Value
		}
	}
	if cookie == "" {
		t.Fatal("no session cookie")
	}
	if got := b.do(http.MethodPost, "/logout", url.Values{"csrf_token": {"wrong"}}); got.Status != http.StatusForbidden {
		t.Fatalf("logout without a CSRF token: %d", got.Status)
	}
	if got := b.get("/logout"); got.Status != http.StatusMethodNotAllowed {
		t.Fatalf("GET /logout: %d", got.Status)
	}
	out := b.post("/logout", nil)
	expect(t, out, http.StatusSeeOther)
	if out.location() != "/login?reason=signed_out" {
		t.Fatalf("location %q", out.location())
	}
	if n := r.count("SELECT COUNT(*) FROM auth_sessions"); n != 0 {
		t.Fatalf("%d sessions", n)
	}
	expect(t, b.get("/ping"), http.StatusSeeOther)
	replay := r.browser()
	expect(t, replay.get("/ping", "Cookie", "atrium_session="+cookie), http.StatusSeeOther)
	expect(t, b.get(out.location()), http.StatusOK, "You are signed out.")
}

func TestOIDCSignOutButtonFollowsTheSession(t *testing.T) {
	r := shapes[0].rig(t)
	b := r.browser()
	b.mustSignIn(r, "member")
	r.exec("UPDATE auth_sessions SET admin = 0")
	page := b.get("/missing")
	expect(t, page, http.StatusNotFound, `action="/logout"`, `aria-label="Sign out"`)
	if strings.Contains(page.Body, `href="/settings`) {
		t.Fatal("a member sees the settings link")
	}
}

func TestOIDCCookieFlags(t *testing.T) {
	r := shapes[0].rig(t)
	plain := r.browser()
	r.idp.Approve("admin")
	start := plain.startLogin("")
	binding := strings.Join(start.Header.Values("Set-Cookie"), "\n")
	for _, want := range []string{"atrium_oidc_state=", "HttpOnly", "SameSite=Lax", "Path=/", "Max-Age=600"} {
		if !strings.Contains(binding, want) {
			t.Fatalf("binding cookie lacks %q: %s", want, binding)
		}
	}
	if strings.Contains(binding, "Secure") {
		t.Fatalf("plain http binding cookie is Secure: %s", binding)
	}
	authz := plain.get(start.location())
	cb := plain.get(authz.location())
	session := strings.Join(cb.Header.Values("Set-Cookie"), "\n")
	for _, want := range []string{"atrium_session=", "HttpOnly", "SameSite=Lax", "Path=/", "Max-Age=2592000"} {
		if !strings.Contains(session, want) {
			t.Fatalf("session cookie lacks %q: %s", want, session)
		}
	}

	secure := r.browser()
	h := []string{"X-Forwarded-Proto", "https"}
	secure.get("/login", h...)
	r.idp.Approve("admin")
	start = secure.post("/login", url.Values{}, h...)
	authz = secure.get(start.location())
	cb = secure.get(authz.location(), h...)
	session = strings.Join(cb.Header.Values("Set-Cookie"), "\n")
	for _, want := range []string{"__Host-atrium_session=", "HttpOnly", "Secure", "SameSite=Lax", "Path=/"} {
		if !strings.Contains(session, want) {
			t.Fatalf("secure session cookie lacks %q: %s", want, session)
		}
	}
	if strings.Contains(session, "Domain=") {
		t.Fatalf("session cookie carries a Domain: %s", session)
	}
}

func TestOIDCUnauthenticatedResponses(t *testing.T) {
	r := shapes[0].rig(t)
	b := r.browser()
	hx := b.get("/ping", "HX-Request", "true")
	expect(t, hx, http.StatusUnauthorized)
	if got := hx.Header.Get("HX-Redirect"); got != "/login" {
		t.Fatalf("HX-Redirect %q", got)
	}
	form := b.do(http.MethodPost, "/settings/ping", url.Values{"a": {"b"}})
	expect(t, form, http.StatusSeeOther)
	if form.location() != "/login" {
		t.Fatalf("location %q", form.location())
	}
	for _, path := range []string{"/", "/chat", "/settings/ping", "/anything/else"} {
		if got := b.get(path); got.Status != http.StatusSeeOther || !strings.HasPrefix(got.location(), "/login") {
			t.Fatalf("%s: %d %q", path, got.Status, got.location())
		}
	}
	login := b.get("/login")
	expect(t, login, http.StatusOK, "<form")
	if !strings.Contains(login.Header.Get("Content-Security-Policy"), "form-action 'self' "+strings.TrimSuffix(r.idp.Issuer(), "/")) {
		t.Fatalf("login CSP %q", login.Header.Get("Content-Security-Policy"))
	}
	expect(t, b.get("/healthz"), http.StatusOK)
	expect(t, b.get("/login?reason=bogus"), http.StatusOK)
	if strings.Contains(b.get("/login?reason=<script>").Body, "<script>") {
		t.Fatal("reason reflected")
	}
}

func TestOIDCPublicPathsStayOpen(t *testing.T) {
	r := newRig(t, rigOpts{preset: "generic", public: func(req *http.Request) bool { return req.URL.Path == "/public/file" }})
	b := r.browser()
	expect(t, b.get("/public/file"), http.StatusOK, "public")
	expect(t, b.get("/ping"), http.StatusSeeOther)
}

func TestOIDCHostIsStillValidated(t *testing.T) {
	r := shapes[0].rig(t)
	b := r.browser()
	b.get("/login")
	req, _ := http.NewRequest(http.MethodGet, r.url+"/login", nil)
	req.Host = "evil.example"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad host: %d", resp.StatusCode)
	}
	cross := b.do(http.MethodPost, "/login", url.Values{"csrf_token": {b.csrf()}}, "Origin", "https://evil.example")
	if cross.Status != http.StatusForbidden {
		t.Fatalf("cross-site login post: %d", cross.Status)
	}
}

func TestAuthModesCombine(t *testing.T) {
	proxy := newRig(t, rigOpts{bare: true, auth: []string{web.AuthProxy}})
	b := proxy.browser()
	expect(t, b.get("/ping"), http.StatusForbidden)
	expect(t, b.get("/login", "Remote-Groups", "atrium_access"), http.StatusNotFound)
	expect(t, b.get("/auth/callback", "Remote-Groups", "atrium_access"), http.StatusNotFound)
	expect(t, b.get("/ping", "Remote-Groups", "atrium_access"), http.StatusOK, "source=proxy", "admin=no")
	expect(t, b.get("/settings/ping", "Remote-Groups", "atrium_access"), http.StatusForbidden, "Admins only")
	expect(t, b.post("/live/resume", nil, "Remote-Groups", "atrium_access"), http.StatusForbidden)
	expect(t, b.get("/settings/ping", "Remote-Groups", "atrium_access, atrium_admin"), http.StatusOK, "admin=yes")
	expect(t, b.post("/settings/ping", nil, "Remote-Groups", "atrium_access,atrium_admin"), http.StatusOK)
	expect(t, b.get("/settings/ping", "Remote-Groups", "atrium_admin"), http.StatusForbidden)
	expect(t, b.get("/ping", "Remote-Groups", "other"), http.StatusForbidden)

	only := shapes[0].rig(t)
	o := only.browser()
	forged := []string{"Remote-Groups", "atrium_access,atrium_admin", "Remote-User", "root"}
	expect(t, o.get("/ping", forged...), http.StatusSeeOther)
	expect(t, o.get("/settings/ping", forged...), http.StatusSeeOther)
	o.mustSignIn(only, "member")
	expect(t, o.get("/settings/ping", forged...), http.StatusForbidden)

	both := newRig(t, rigOpts{preset: "generic", auth: []string{web.AuthProxy, web.AuthOIDC}})
	both.idp.AddUser(oidctest.User{Name: "member", Groups: []string{"atrium_access"}})
	c := both.browser()
	expect(t, c.get("/ping", "Remote-Groups", "atrium_access"), http.StatusOK, "source=proxy")
	expect(t, c.get("/ping"), http.StatusSeeOther)
	c.mustSignIn(both, "member")
	expect(t, c.get("/ping"), http.StatusOK, "source=oidc", "admin=no")
	expect(t, c.get("/ping", "Remote-Groups", "atrium_access"), http.StatusOK, "source=proxy", "admin=no")
	expect(t, c.get("/ping", "Remote-Groups", "atrium_access,atrium_admin"), http.StatusOK, "source=proxy", "admin=yes")
}

func TestProxyAdminGroupIsConfigurable(t *testing.T) {
	r := newRig(t, rigOpts{preset: "generic", auth: []string{web.AuthProxy}, settings: map[string]string{"oidc_admin_group": "operators"}})
	b := r.browser()
	expect(t, b.get("/settings/ping", "Remote-Groups", "atrium_access,atrium_admin"), http.StatusForbidden)
	expect(t, b.get("/settings/ping", "Remote-Groups", "atrium_access,operators"), http.StatusOK)
}

func TestOIDCDomainAllowlists(t *testing.T) {
	r := newRig(t, rigOpts{preset: "google", settings: map[string]string{"oidc_access_domains": "example.com", "oidc_admin_domains": "ops.example.org"}})
	r.idp.AddUser(oidctest.User{Name: "staff", Email: "a@Example.com", EmailVerified: true})
	r.idp.AddUser(oidctest.User{Name: "ops", Email: "root@ops.example.org", EmailVerified: true})
	r.idp.AddUser(oidctest.User{Name: "lookalike", Email: "a@notexample.com", EmailVerified: true})
	r.idp.AddUser(oidctest.User{Name: "unverified", Email: "a@example.com"})
	staff := r.browser()
	staff.mustSignIn(r, "staff")
	expect(t, staff.get("/settings/ping"), http.StatusForbidden)
	ops := r.browser()
	ops.mustSignIn(r, "ops")
	expect(t, ops.get("/settings/ping"), http.StatusOK)
	expect(t, r.browser().signIn(r, "lookalike"), http.StatusForbidden)
	expect(t, r.browser().signIn(r, "unverified"), http.StatusForbidden)
}

func TestLocalModeStaysOpenAndAdmin(t *testing.T) {
	r := newRig(t, rigOpts{bare: true, auth: []string{web.AuthLocal}})
	b := r.browser()
	expect(t, b.get("/settings/ping"), http.StatusOK, "source=local", "admin=yes")
	expect(t, b.get("/login"), http.StatusNotFound)
}

func TestOIDCNotConfigured(t *testing.T) {
	r := newRig(t, rigOpts{bare: true})
	b := r.browser()
	page := b.get("/login")
	expect(t, page, http.StatusOK, "Sign-in is not set up.")
	if strings.Contains(page.Body, "<form") {
		t.Fatal("a start form is offered with no provider")
	}
	expect(t, b.get("/ping"), http.StatusSeeOther)
	expect(t, b.startLogin(""), http.StatusOK, "Sign-in is not set up.")
}

func TestOIDCProviderUnreachable(t *testing.T) {
	r := shapes[0].rig(t)
	r.idp.Down = true
	b := r.browser()
	expect(t, b.startLogin(""), http.StatusBadGateway, "The provider did not respond.")
	r.idp.Down = false
	r.browser().mustSignIn(r, "admin")
}

func TestOIDCSessionTablesMigrateAGoldenDatabase(t *testing.T) {
	ctx := context.Background()
	src := filepath.Join(golden.Dir(), "atrium.db")
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "atrium.db")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	d, err := db.Connect(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	tables := []string{"threads", "messages", "settings", "proposals", "usage"}
	counts := func() map[string]int {
		out := map[string]int{}
		for _, table := range tables {
			var n int
			if err := d.Read.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&n); err != nil {
				continue
			}
			out[table] = n
		}
		return out
	}
	before := counts()
	if err := db.Migrate(ctx, d, path); err != nil {
		t.Fatal(err)
	}
	after := counts()
	for table, n := range before {
		if after[table] != n {
			t.Fatalf("%s rows changed: %d -> %d", table, n, after[table])
		}
	}
	if len(before) == 0 {
		t.Fatal("golden database holds none of the expected tables")
	}
	r := newRig(t, rigOpts{preset: "generic", db: d})
	r.idp.AddUser(oidctest.User{Name: "admin", Groups: []string{"atrium_admin"}})
	b := r.browser()
	b.mustSignIn(r, "admin")
	expect(t, b.get("/settings/ping"), http.StatusOK, "admin=yes")
	var applied int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM schema_migrations WHERE version = 8").Scan(&applied); err != nil || applied != 1 {
		t.Fatalf("migration 8 applied %d: %v", applied, err)
	}
	if err := db.Migrate(ctx, d, path); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	expect(t, b.get("/ping"), http.StatusOK)
}

func runServe(t *testing.T, args ...string) (int, string) {
	t.Helper()
	t.Setenv("COLUMNS", "")
	t.Setenv("ATRIUM_PUBLIC_HOST", "")
	app := cli.New("atrium", nil)
	app.Add(web.ServeCommand())
	var stdout, stderr bytes.Buffer
	code := app.Run(context.Background(), append([]string{"serve"}, args...), &stdout, &stderr)
	return code, stderr.String()
}

func TestServeAuthFlags(t *testing.T) {
	code, stderr := runServe(t, "--port", "1", "--auth", "oidc")
	if code != cli.ExitError || !strings.Contains(stderr, "oidc mode needs --allowed-host or ATRIUM_PUBLIC_HOST") {
		t.Errorf("oidc without a host: %d %q", code, stderr)
	}
	code, stderr = runServe(t, "--port", "1", "--auth", "local", "--auth", "oidc", "--allowed-host", "x")
	if code != cli.ExitError || !strings.Contains(stderr, "local cannot combine") {
		t.Errorf("local with oidc: %d %q", code, stderr)
	}
	code, stderr = runServe(t, "--port", "1", "--auth", "basic")
	if code == cli.ExitOK || stderr == "" {
		t.Errorf("unknown mode: %d %q", code, stderr)
	}
}

var _ = oidc.CallbackPath

package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/auth/loginlink"
	"github.com/jeiang/atrium/internal/auth/oidc"
	"github.com/jeiang/atrium/internal/auth/oidc/oidctest"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/secrets"
	"github.com/jeiang/atrium/internal/settings"
	"github.com/jeiang/atrium/internal/testutil/golden"
	"github.com/jeiang/atrium/internal/web"
	"github.com/jeiang/atrium/internal/web/tpl"
)

type linkSite struct {
	t     *testing.T
	base  string
	db    *db.DB
	clock *time.Time
	idp   *oidctest.Provider
}

type linkReply struct {
	status int
	header http.Header
	body   string
}

type linkBrowser struct {
	t    *testing.T
	site *linkSite
	jar  *cookiejar.Jar
	http *http.Client
}

func (s *linkSite) browser() *linkBrowser {
	jar, err := cookiejar.New(nil)
	if err != nil {
		s.t.Fatal(err)
	}
	return &linkBrowser{t: s.t, site: s, jar: jar, http: &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (b *linkBrowser) do(method, target string, form url.Values) linkReply {
	b.t.Helper()
	if !strings.HasPrefix(target, "http") {
		target = b.site.base + target
	}
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequest(method, target, body)
	if err != nil {
		b.t.Fatal(err)
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", b.site.base)
	}
	resp, err := b.http.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		b.t.Fatal(err)
	}
	return linkReply{resp.StatusCode, resp.Header, string(data)}
}

func (b *linkBrowser) get(target string) linkReply { return b.do(http.MethodGet, target, nil) }

func (b *linkBrowser) cookie(name string) string {
	u, _ := url.Parse(b.site.base)
	for _, c := range b.jar.Cookies(u) {
		if c.Name == name {
			return c.Value
		}
	}
	return ""
}

func (b *linkBrowser) post(target string, form url.Values) linkReply {
	b.t.Helper()
	if b.cookie(web.CSRFCookie) == "" {
		b.get("/login/link")
	}
	values := url.Values{"csrf_token": {b.cookie(web.CSRFCookie)}}
	for k, v := range form {
		values[k] = v
	}
	return b.do(http.MethodPost, target, values)
}

func (b *linkBrowser) sessionToken() string { return b.cookie("atrium_session") }

func newLinkSite(t *testing.T) *linkSite {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "atrium.db")
	listener := httptest.NewUnstartedServer(nil)
	base := "http://" + listener.Listener.Addr().String()
	t.Setenv("ATRIUM_DB", path)
	t.Setenv("ATRIUM_CACHE_DIR", filepath.Join(dir, "cache"))
	t.Setenv("ATRIUM_MODE", "shadow")
	t.Setenv("ATRIUM_CORE_PUBLIC_URL", base)
	t.Setenv(settingEnv(secrets.OIDCClientSecret), "s3cret-for-the-test")
	t.Cleanup(func() { settings.Default.SetPins(nil) })
	site := &linkSite{t: t, base: base}
	site.idp = oidctest.New(t, oidctest.Options{ClientSecret: "s3cret-for-the-test"})
	site.idp.AddUser(oidctest.User{Name: "owner", Email: "owner@example.com", Groups: []string{"atrium_access", "atrium_admin"}})
	site.idp.AddUser(oidctest.User{Name: "guest", Email: "guest@example.com", Groups: []string{"atrium_access"}})
	if out := site.link(t, "--minutes", "1"); out == "" {
		t.Fatal("no link printed while migrating")
	}
	d, err := db.Connect(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	site.db = d
	cfg, err := config.Load(config.OS)
	if err != nil {
		t.Fatal(err)
	}
	render, err := web.NewRenderer(cfg, d, tpl.Config{})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	site.clock = &now
	links := loginlink.New(d)
	links.Now = func() time.Time { return *site.clock }
	app, err := web.New(web.Options{
		Cfg: cfg, Env: config.OS, DB: d, Auth: []string{web.AuthOIDC}, AllowedHosts: []string{"127.0.0.1"},
		Render: render, OIDC: oidc.New(d, config.OS), LoginLinks: links,
	})
	if err != nil {
		t.Fatal(err)
	}
	listener.Config.Handler = app
	listener.Start()
	t.Cleanup(func() {
		listener.Close()
		app.Close()
	})
	return site
}

func (s *linkSite) link(t *testing.T, args ...string) string {
	t.Helper()
	var out, errs bytes.Buffer
	code := newApp().Run(context.Background(), append([]string{"login-link"}, args...), &out, &errs)
	if code != cli.ExitOK {
		t.Fatalf("login-link exit %d: %s", code, errs.String())
	}
	if strings.Contains(errs.String(), "#") || strings.Count(out.String(), "\n") != 1 {
		t.Fatalf("unexpected output %q %q", out.String(), errs.String())
	}
	return strings.TrimSpace(out.String())
}

func fragmentOf(t *testing.T, link, base string) string {
	t.Helper()
	token, ok := strings.CutPrefix(link, base+"/login/link#")
	if !ok || token == "" {
		t.Fatalf("link %q does not start with %q", link, base+"/login/link#")
	}
	return token
}

func (s *linkSite) sessions() int {
	var n int
	if err := s.db.Read.QueryRow("SELECT COUNT(*) FROM auth_sessions").Scan(&n); err != nil {
		s.t.Fatal(err)
	}
	return n
}

func linkExpect(t *testing.T, got linkReply, status int, contains ...string) {
	t.Helper()
	if got.status != status {
		t.Fatalf("status %d, want %d; location %q body %.300q", got.status, status, got.header.Get("Location"), got.body)
	}
	for _, want := range contains {
		if !strings.Contains(got.body, want) {
			t.Fatalf("body lacks %q: %.400q", want, got.body)
		}
	}
}

func TestLoginLinkOpensAnAdminSessionOnceWithNoOIDCConfigured(t *testing.T) {
	site := newLinkSite(t)
	token := fragmentOf(t, site.link(t), site.base)

	b := site.browser()
	landing := b.get("/login/link")
	linkExpect(t, landing, http.StatusOK, "Continue", `data-link-form`)
	if strings.Contains(landing.body, token) || landing.header.Get("Cache-Control") != "no-store" {
		t.Fatal("the landing page leaks the token or is cacheable")
	}
	if site.sessions() != 0 {
		t.Fatal("opening the landing page created a session")
	}
	redeemed := b.post("/login/link", url.Values{"token": {token}})
	linkExpect(t, redeemed, http.StatusSeeOther)
	if redeemed.header.Get("Location") != "/settings/auth" || b.sessionToken() == "" || site.sessions() != 1 {
		t.Fatalf("redeem: location %q sessions %d", redeemed.header.Get("Location"), site.sessions())
	}
	linkExpect(t, b.get("/settings/auth"), http.StatusOK, "Not set up", "Test sign-in")

	again := site.browser()
	linkExpect(t, again.post("/login/link", url.Values{"token": {token}}), http.StatusGone, "This link was already used.", "atrium login-link")
	if again.sessionToken() != "" || site.sessions() != 1 {
		t.Fatal("a second use created a session")
	}
	linkExpect(t, b.get("/settings/auth"), http.StatusOK)
	linkExpect(t, again.get("/settings/auth"), http.StatusSeeOther)
}

func TestLoginLinkExpiresAndRejectsTampering(t *testing.T) {
	site := newLinkSite(t)
	token := fragmentOf(t, site.link(t, "--minutes", "1"), site.base)
	nonce, mac, _ := strings.Cut(token, ".")
	flip := func(s string) string {
		if s[0] == 'A' {
			return "B" + s[1:]
		}
		return "A" + s[1:]
	}
	for name, bad := range map[string]string{
		"mac":      nonce + "." + flip(mac),
		"nonce":    flip(nonce) + "." + mac,
		"no mac":   nonce,
		"empty":    "",
		"reversed": mac + "." + nonce,
	} {
		b := site.browser()
		linkExpect(t, b.post("/login/link", url.Values{"token": {bad}}), http.StatusBadRequest, "This link is not valid.")
		if b.sessionToken() != "" {
			t.Fatalf("%s: tampered link created a session", name)
		}
	}
	if site.sessions() != 0 {
		t.Fatal("sessions exist after refused links")
	}

	*site.clock = site.clock.Add(2 * time.Minute)
	late := site.browser()
	linkExpect(t, late.post("/login/link", url.Values{"token": {token}}), http.StatusGone, "This link expired.")
	if late.sessionToken() != "" || site.sessions() != 0 {
		t.Fatal("an expired link created a session")
	}

	fresh := fragmentOf(t, site.link(t, "--minutes", "10"), site.base)
	linkExpect(t, site.browser().post("/login/link", url.Values{"token": {fresh}}), http.StatusSeeOther)
	if site.sessions() != 1 {
		t.Fatal("a valid link did not open a session")
	}
}

func TestLoginLinkIssueAndRedeemAreAuditedWithoutTheToken(t *testing.T) {
	site := newLinkSite(t)
	token := fragmentOf(t, site.link(t), site.base)
	nonce, mac, _ := strings.Cut(token, ".")
	site.browser().post("/login/link", url.Values{"token": {token}})
	site.browser().post("/login/link", url.Values{"token": {token}})
	site.browser().post("/login/link", url.Values{"token": {"garbage"}})

	rows, err := site.db.Read.Query("SELECT new_value, actor, source FROM settings_audit WHERE key = 'login_link' ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var got []string
	for rows.Next() {
		var value, actor, source string
		if err := rows.Scan(&value, &actor, &source); err != nil {
			t.Fatal(err)
		}
		got = append(got, source+" "+actor+": "+value)
	}
	prefixes := []string{"cli operator: issued #1", "cli operator: issued #2", "ui login-link: redeemed #2", "ui login-link: refused: #2 was already used", "ui login-link: refused: not valid"}
	if len(got) != len(prefixes) {
		t.Fatalf("audit trail %q", got)
	}
	for i, prefix := range prefixes {
		if !strings.HasPrefix(got[i], prefix) || strings.Contains(got[i], nonce) || strings.Contains(got[i], mac) {
			t.Fatalf("audit row %d = %q, want prefix %q and no token", i, got[i], prefix)
		}
	}
	var stored string
	if err := site.db.Read.QueryRow("SELECT nonce_hash FROM auth_login_links WHERE id = 2").Scan(&stored); err != nil || strings.Contains(stored, nonce) {
		t.Fatalf("nonce stored as %q: %v", stored, err)
	}
}

func TestLoginLinkRefusesBadLifetimesAndMissingPublicURL(t *testing.T) {
	newLinkSite(t)
	for _, minutes := range []string{"0", "999"} {
		var out, errs bytes.Buffer
		if code := newApp().Run(context.Background(), []string{"login-link", "--minutes", minutes}, &out, &errs); code != cli.ExitUsage || out.Len() != 0 {
			t.Fatalf("%s minutes: %d %q", minutes, code, out.String())
		}
	}
	t.Setenv("ATRIUM_CORE_PUBLIC_URL", "")
	var out, errs bytes.Buffer
	if code := newApp().Run(context.Background(), []string{"login-link"}, &out, &errs); code != cli.ExitError || out.Len() != 0 || !strings.Contains(errs.String(), "public") {
		t.Fatalf("no public URL: %d %q %q", code, out.String(), errs.String())
	}
}

func TestLoginLinkFirstRunSetupThenOIDCSignIn(t *testing.T) {
	site := newLinkSite(t)
	owner := site.browser()
	linkExpect(t, owner.post("/login/link", url.Values{"token": {fragmentOf(t, site.link(t), site.base)}}), http.StatusSeeOther)
	before := owner.sessionToken()

	anon := site.browser()
	linkExpect(t, anon.get("/login"), http.StatusOK, "Sign-in is not set up.")

	form := url.Values{
		"preset": {"pocket_id"}, "issuer": {site.idp.Issuer()}, "client_id": {site.idp.ClientID()},
		"access_group": {"atrium_access"}, "admin_group": {"atrium_admin"}, "groups_claim": {"groups"}, "scopes": {"openid profile email groups"},
	}
	providerTab := func(user string) linkReply {
		site.idp.Approve(user)
		start := owner.post("/settings/auth/test", form)
		linkExpect(t, start, http.StatusOK)
		authz := owner.get(strings.TrimPrefix(start.header.Get("Refresh"), "0; url="))
		return owner.get(authz.header.Get("Location"))
	}
	linkExpect(t, providerTab("guest"), http.StatusForbidden, "This account is not in `atrium_admin`.")
	linkExpect(t, anon.get("/login"), http.StatusOK, "Sign-in is not set up.")

	linkExpect(t, providerTab("owner"), http.StatusOK, "Test passed")
	status := owner.get("/settings/auth/test/status")
	linkExpect(t, status, http.StatusOK, "Activate")
	fingerprint := regexp.MustCompile(`name="fingerprint" value="([0-9a-f]+)"`).FindStringSubmatch(status.body)[1]
	linkExpect(t, owner.post("/settings/auth/activate", url.Values{"fingerprint": {fingerprint}}), http.StatusOK, "Sign-in is active", "Pocket ID")

	if owner.sessionToken() != before {
		t.Fatal("activation replaced the link session")
	}
	linkExpect(t, owner.get("/settings/auth"), http.StatusOK, "Active")

	user := site.browser()
	linkExpect(t, user.get("/login"), http.StatusOK, "Sign in with Pocket ID")
	site.idp.Approve("owner")
	begin := user.post("/login", url.Values{"next": {"/settings/auth"}})
	linkExpect(t, begin, http.StatusSeeOther)
	authz := user.get(begin.header.Get("Location"))
	linkExpect(t, user.get(authz.header.Get("Location")), http.StatusSeeOther)
	linkExpect(t, user.get("/settings/auth"), http.StatusOK, "Active")
}

func TestLoginLinkMigratesAGoldenDatabaseCopy(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(golden.Dir(), "atrium.db"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "atrium.db")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ATRIUM_DB", path)
	t.Setenv("ATRIUM_CACHE_DIR", filepath.Join(dir, "cache"))
	t.Setenv("ATRIUM_CORE_PUBLIC_URL", "https://atrium.example")
	t.Cleanup(func() { settings.Default.SetPins(nil) })

	counts := func(d *db.DB) map[string]int {
		out := map[string]int{}
		for _, table := range []string{"threads", "messages", "settings", "proposals", "usage"} {
			var n int
			if err := d.Read.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err == nil {
				out[table] = n
			}
		}
		return out
	}
	raw, err := db.Connect(path)
	if err != nil {
		t.Fatal(err)
	}
	before := counts(raw)
	_ = raw.Close()
	if len(before) == 0 {
		t.Fatal("golden database holds none of the expected tables")
	}

	var out, errs bytes.Buffer
	if code := newApp().Run(context.Background(), []string{"login-link"}, &out, &errs); code != cli.ExitOK {
		t.Fatalf("login-link on a golden copy: %d %s", code, errs.String())
	}
	if !strings.HasPrefix(out.String(), "https://atrium.example/login/link#") {
		t.Fatalf("link %q", out.String())
	}
	migrated, err := db.Connect(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = migrated.Close() }()
	for table, n := range before {
		if got := counts(migrated)[table]; got != n {
			t.Fatalf("%s rows changed: %d -> %d", table, n, got)
		}
	}
	var version, links int
	if err := migrated.Read.QueryRow("SELECT COUNT(*) FROM schema_migrations WHERE version = 9").Scan(&version); err != nil || version != 1 {
		t.Fatalf("migration 9 applied %d: %v", version, err)
	}
	if err := migrated.Read.QueryRow("SELECT COUNT(*) FROM auth_login_links").Scan(&links); err != nil || links != 1 {
		t.Fatalf("links %d: %v", links, err)
	}
	if _, err := migrated.Write.Exec("INSERT INTO auth_sessions (token_hash, subject, admin, created_at, last_seen_at, idle_expires_at, absolute_expires_at, rechecked_at) VALUES ('h', 's', 0, 1, 1, 2, 2, 1)"); err != nil {
		t.Fatal(err)
	}
	var origin string
	if err := migrated.Read.QueryRow("SELECT origin FROM auth_sessions WHERE token_hash = 'h'").Scan(&origin); err != nil || origin != "oidc" {
		t.Fatalf("session origin %q: %v", origin, err)
	}
	out.Reset()
	if code := newApp().Run(context.Background(), []string{"login-link"}, &out, &errs); code != cli.ExitOK {
		t.Fatalf("second login-link: %d %s", code, errs.String())
	}
}

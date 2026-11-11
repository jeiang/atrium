package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/auth/oidc/oidctest"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/testutil/golden"
	"github.com/jeiang/atrium/internal/web"
)

const linkPublic = "http://127.0.0.1"

type linkSite struct {
	t    *testing.T
	base string
	db   *db.DB
	idp  *oidctest.Provider
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
	if rest, ok := strings.CutPrefix(target, linkPublic+"/"); ok {
		target = "/" + rest
	}
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

func linkEnv(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "atrium.db")
	setEnv(t, "ATRIUM_DB", path)
	setEnv(t, "ATRIUM_CACHE_DIR", filepath.Join(dir, "cache"))
	setEnv(t, "ATRIUM_PUBLIC_HOST", "")
	setEnv(t, "ATRIUM_MODE", "shadow")
	setEnv(t, "ATRIUM_CORE_PUBLIC_URL", linkPublic)
	return path
}

func newLinkSite(t *testing.T) *linkSite {
	t.Helper()
	path := linkEnv(t)
	site := &linkSite{t: t}
	site.idp = oidctest.New(t, oidctest.Options{ClientSecret: "s3cret-for-the-test"})
	site.idp.AddUser(oidctest.User{Name: "owner", Email: "owner@example.com", Groups: []string{"atrium_access", "atrium_admin"}})
	site.idp.AddUser(oidctest.User{Name: "guest", Email: "guest@example.com", Groups: []string{"atrium_access"}})
	if out := site.link(t, "--minutes", "1"); out == "" {
		t.Fatal("no link printed while migrating")
	}
	base, _, stop, err := startServeAppArgs(t, newTestApp(t, nil), &lockedBuffer{}, "--auth", "oidc", "--allowed-host", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	site.base = base
	d := mustConnect(t, path)
	t.Cleanup(func() { _ = d.Close() })
	site.db = d
	return site
}

func (s *linkSite) link(t *testing.T, args ...string) string {
	t.Helper()
	got := runApp(t, append([]string{"login-link"}, args...)...)
	if got.code != cli.ExitOK {
		t.Fatalf("login-link exit %d: %s", got.code, got.stderr)
	}
	if strings.Contains(got.stderr, "#") || strings.Count(got.stdout, "\n") != 1 {
		t.Fatalf("unexpected output %q %q", got.stdout, got.stderr)
	}
	return strings.TrimSpace(got.stdout)
}

func fragmentOf(t *testing.T, link string) string {
	t.Helper()
	token, ok := strings.CutPrefix(link, linkPublic+"/login/link#")
	if !ok || token == "" {
		t.Fatalf("link %q does not start with %q", link, linkPublic+"/login/link#")
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
	t.Parallel()
	site := newLinkSite(t)
	token := fragmentOf(t, site.link(t))

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
	if redeemed.header.Get("Location") != "/chat" || b.sessionToken() == "" || site.sessions() != 1 {
		t.Fatalf("redeem: location %q sessions %d", redeemed.header.Get("Location"), site.sessions())
	}
	linkExpect(t, b.get("/settings/auth"), http.StatusOK, "oidc is not configured")

	again := site.browser()
	linkExpect(t, again.post("/login/link", url.Values{"token": {token}}), http.StatusGone, "This link was already used.", "atrium login-link")
	if again.sessionToken() != "" || site.sessions() != 1 {
		t.Fatal("a second use created a session")
	}
	linkExpect(t, b.get("/settings/auth"), http.StatusOK)
	linkExpect(t, again.get("/settings/auth"), http.StatusSeeOther)
}

func TestLoginLinkExpiresAndRejectsTampering(t *testing.T) {
	t.Parallel()
	site := newLinkSite(t)
	token := fragmentOf(t, site.link(t, "--minutes", "1"))
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

	if _, err := site.db.Write.Exec("UPDATE auth_login_links SET expires_at = issued_at - 1 WHERE used_at IS NULL"); err != nil {
		t.Fatal(err)
	}
	late := site.browser()
	linkExpect(t, late.post("/login/link", url.Values{"token": {token}}), http.StatusGone, "This link expired.")
	if late.sessionToken() != "" || site.sessions() != 0 {
		t.Fatal("an expired link created a session")
	}

	fresh := fragmentOf(t, site.link(t, "--minutes", "10"))
	linkExpect(t, site.browser().post("/login/link", url.Values{"token": {fresh}}), http.StatusSeeOther)
	if site.sessions() != 1 {
		t.Fatal("a valid link did not open a session")
	}
}

func TestLoginLinkIssueAndRedeemAreAuditedWithoutTheToken(t *testing.T) {
	t.Parallel()
	site := newLinkSite(t)
	token := fragmentOf(t, site.link(t))
	nonce, mac, _ := strings.Cut(token, ".")
	site.browser().post("/login/link", url.Values{"token": {token}})
	site.browser().post("/login/link", url.Values{"token": {token}})
	site.browser().post("/login/link", url.Values{"token": {"garbage"}})
	site.browser().post("/login/link", url.Values{"token": {nonce + ".AAAA"}})

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
	prefixes := []string{"cli operator: issued #1", "cli operator: issued #2", "ui login-link: redeemed #2", "ui login-link: refused: #2 was already used"}
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
	t.Parallel()
	linkEnv(t)
	for _, minutes := range []string{"0", "999"} {
		var out, errs bytes.Buffer
		if code := defaultApp(t).Run(context.Background(), []string{"login-link", "--minutes", minutes}, &out, &errs); code != cli.ExitUsage || out.Len() != 0 {
			t.Fatalf("%s minutes: %d %q", minutes, code, out.String())
		}
	}
	setEnv(t, "ATRIUM_CORE_PUBLIC_URL", "")
	var out, errs bytes.Buffer
	if code := defaultApp(t).Run(context.Background(), []string{"login-link"}, &out, &errs); code != cli.ExitError || out.Len() != 0 || !strings.Contains(errs.String(), "public") {
		t.Fatalf("no public URL: %d %q %q", code, out.String(), errs.String())
	}
}

func TestLoginLinkWorksOnAGoldenDatabaseCopy(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "atrium.db")
	setEnv(t, "ATRIUM_DB", path)
	setEnv(t, "ATRIUM_CACHE_DIR", filepath.Join(dir, "cache"))
	setEnv(t, "ATRIUM_CORE_PUBLIC_URL", "https://atrium.example")
	copyFile(t, filepath.Join(golden.Dir(), "world.db"), path)

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
	if code := defaultApp(t).Run(context.Background(), []string{"login-link"}, &out, &errs); code != cli.ExitOK {
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
	if err := migrated.Read.QueryRow("SELECT COUNT(*) FROM schema_migrations WHERE version = 1").Scan(&version); err != nil || version != 1 {
		t.Fatalf("baseline migration applied %d: %v", version, err)
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
	if code := defaultApp(t).Run(context.Background(), []string{"login-link"}, &out, &errs); code != cli.ExitOK {
		t.Fatalf("second login-link: %d %s", code, errs.String())
	}
}

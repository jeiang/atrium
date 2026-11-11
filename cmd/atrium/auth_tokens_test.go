package main

import (
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/auth/oidc/oidctest"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/mail/imapclient"
	"github.com/jeiang/atrium/internal/settings"
)

var tokenHashShape = regexp.MustCompile(`^[0-9a-f]{64}$`)

func authTokenApp(t *testing.T) *cli.App {
	t.Helper()
	f := fixtureFor(t)
	if f == nil {
		t.Fatal("no fixture")
	}
	reg := settings.New()
	return newAppWith(f.lookup, reg, config.NewState(reg), nil, imapclient.Opener, nil, quietLog)
}

func tokenRows(t *testing.T, out string) [][]string {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) == 0 || !strings.HasPrefix(lines[0], "ID\t") {
		t.Fatalf("token list has no header:\n%s", out)
	}
	var rows [][]string
	for _, line := range lines[1:] {
		rows = append(rows, strings.Split(line, "\t"))
	}
	return rows
}

func tokenRow(t *testing.T, out, name string) []string {
	t.Helper()
	for _, row := range tokenRows(t, out) {
		if len(row) > 1 && row[1] == name {
			return row
		}
	}
	t.Fatalf("no token named %q in:\n%s", name, out)
	return nil
}

func TestAuthTokensIssueListAndRevokeThroughTheCLI(t *testing.T) {
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))

	first := runApp(t, "auth", "token", "issue", "--name", "phone")
	if first.code != cli.ExitOK {
		t.Fatalf("issue: exit %d stdout %q stderr %q", first.code, first.stdout, first.stderr)
	}
	secret := strings.TrimSpace(first.stdout)
	if len(secret) != 47 || !strings.HasPrefix(secret, "atr_") {
		t.Fatalf("issue printed %q, not an atr_ secret", first.stdout)
	}
	if strings.Contains(first.stderr, secret) {
		t.Fatalf("the secret leaked to stderr: %q", first.stderr)
	}
	if !strings.Contains(first.stderr, "issued token 1 (local)") {
		t.Fatalf("issue metadata: %q", first.stderr)
	}

	expiry := time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)
	second := runApp(t, "auth", "token", "issue", "--name", "laptop", "--expires-at", expiry)
	if second.code != cli.ExitOK {
		t.Fatalf("issue with expiry: exit %d stdout %q stderr %q", second.code, second.stdout, second.stderr)
	}

	for _, bad := range [][]string{
		{"auth", "token", "issue"},
		{"auth", "token", "issue", "--name", " "},
		{"auth", "token", "issue", "--name", strings.Repeat("x", 101)},
		{"auth", "token", "issue", "--name", "old", "--expires-at", time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)},
		{"auth", "token", "issue", "--name", "bad", "--expires-at", "tomorrow"},
	} {
		if got := runApp(t, bad...); got.code != cli.ExitUsage {
			t.Errorf("%v: exit %d, want usage %d (stdout %q stderr %q)", bad, got.code, cli.ExitUsage, got.stdout, got.stderr)
		}
	}

	listed := runApp(t, "auth", "token", "list")
	if listed.code != cli.ExitOK {
		t.Fatalf("list: exit %d stderr %q", listed.code, listed.stderr)
	}
	if strings.Contains(listed.stdout, secret) || strings.Contains(listed.stdout, strings.TrimSpace(second.stdout)) {
		t.Fatalf("the list exposes a secret:\n%s", listed.stdout)
	}
	phone := tokenRow(t, listed.stdout, "phone")
	if len(phone) != 7 || phone[0] != "1" || phone[2] != "local" || phone[3] == "" || phone[4] != "-" || phone[5] != "-" || phone[6] != "-" {
		t.Fatalf("phone row: %v", phone)
	}
	laptop := tokenRow(t, listed.stdout, "laptop")
	if laptop[4] == "-" {
		t.Fatalf("the expiring token has no expiry: %v", laptop)
	}

	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
	defer func() { _ = d.Close() }()
	var hash string
	if err := d.Read.QueryRow("SELECT token_hash FROM auth_bearers WHERE id = 1").Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if !tokenHashShape.MatchString(hash) || hash == secret {
		t.Fatalf("stored token hash %q is not a SHA-256 hex digest", hash)
	}
	var plain int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM auth_bearers WHERE token_hash = ?", secret).Scan(&plain); err != nil || plain != 0 {
		t.Fatalf("the database stores the plaintext secret: %d, %v", plain, err)
	}

	if revoked := runApp(t, "auth", "token", "revoke", "1"); revoked.code != cli.ExitOK || !strings.Contains(revoked.stdout, "revoked 1") {
		t.Fatalf("revoke: exit %d stdout %q stderr %q", revoked.code, revoked.stdout, revoked.stderr)
	}
	if again := runApp(t, "auth", "token", "revoke", "1"); again.code != cli.ExitOK {
		t.Fatalf("revoking a revoked token is not idempotent: exit %d stderr %q", again.code, again.stderr)
	}
	if missing := runApp(t, "auth", "token", "revoke", "999"); missing.code != cli.ExitError {
		t.Fatalf("revoking an unknown id: exit %d, want %d", missing.code, cli.ExitError)
	}
	if bad := runApp(t, "auth", "token", "revoke", "x"); bad.code != cli.ExitUsage {
		t.Fatalf("revoking a non-numeric id: exit %d, want usage", bad.code)
	}
	phone = tokenRow(t, runApp(t, "auth", "token", "list").stdout, "phone")
	if phone[6] == "-" {
		t.Fatalf("the revoked token has no revocation time: %v", phone)
	}

	var audits, leaked int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM settings_audit WHERE key IN ('auth_token.issue', 'auth_token.revoke')").Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if audits < 3 {
		t.Fatalf("issuance and revocation left %d audit rows, want at least 3", audits)
	}
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM settings_audit WHERE new_value LIKE '%atr_%' OR old_value LIKE '%atr_%'").Scan(&leaked); err != nil {
		t.Fatal(err)
	}
	if leaked != 0 {
		t.Fatalf("%d audit rows hold a token secret", leaked)
	}
}

func TestAuthTokenLocalBearerLifecycle(t *testing.T) {
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))

	issued := runApp(t, "auth", "token", "issue", "--name", "phone")
	if issued.code != cli.ExitOK {
		t.Fatalf("issue: exit %d stderr %q", issued.code, issued.stderr)
	}
	secret := strings.TrimSpace(issued.stdout)
	shortExpiry := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	expiring := runApp(t, "auth", "token", "issue", "--name", "short", "--expires-at", shortExpiry)
	if expiring.code != cli.ExitOK {
		t.Fatalf("issue with expiry: exit %d stderr %q", expiring.code, expiring.stderr)
	}
	short := strings.TrimSpace(expiring.stdout)

	base, _, stop, err := startServeAppArgs(t, fixtureFor(t).app, &lockedBuffer{}, "--auth", "local")
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	api := &apiSite{t: t, base: base}

	if me := api.do(http.MethodGet, "/api/v1/me", secret, nil); me.status != http.StatusOK {
		t.Fatalf("a local token cannot read the API: %d %s", me.status, me.body)
	}
	if page := api.do(http.MethodGet, "/settings/auth", "", nil); page.status != http.StatusOK || !strings.Contains(page.body, "API tokens") {
		t.Fatalf("the token settings page is not reachable in local mode: %d %.300s", page.status, page.body)
	}

	phone := tokenRow(t, runImportApp(t, authTokenApp(t), "auth", "token", "list").stdout, "phone")
	if phone[5] == "-" {
		t.Fatalf("a successful API call did not record last use: %v", phone)
	}
	before := phone[5]
	if bad := api.do(http.MethodGet, "/api/v1/me", "atr_"+strings.Repeat("A", 43), nil); bad.status != http.StatusUnauthorized || bad.code() != "invalid_token" {
		t.Fatalf("an unknown atr_ token: %d %s", bad.status, bad.body)
	}
	phone = tokenRow(t, runImportApp(t, authTokenApp(t), "auth", "token", "list").stdout, "phone")
	if phone[5] != before {
		t.Fatalf("a refused request changed last use: %q -> %q", before, phone[5])
	}

	if revoked := runImportApp(t, authTokenApp(t), "auth", "token", "revoke", phone[0]); revoked.code != cli.ExitOK {
		t.Fatalf("revoke: exit %d stderr %q", revoked.code, revoked.stderr)
	}
	if refused := api.do(http.MethodGet, "/api/v1/me", secret, nil); refused.status != http.StatusUnauthorized || refused.code() != "invalid_token" {
		t.Fatalf("a revoked local token was admitted: %d %s", refused.status, refused.body)
	}

	if ok := api.do(http.MethodGet, "/api/v1/me", short, nil); ok.status != http.StatusOK {
		t.Fatalf("the expiring token is refused before its expiry: %d %s", ok.status, ok.body)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
	defer func() { _ = d.Close() }()
	if _, err := d.Write.Exec("UPDATE auth_bearers SET expires_at = ? WHERE name = 'short'", float64(time.Now().Add(-time.Minute).Unix())); err != nil {
		t.Fatal(err)
	}
	if expired := api.do(http.MethodGet, "/api/v1/me", short, nil); expired.status != http.StatusUnauthorized || expired.code() != "invalid_token" {
		t.Fatalf("an expired local token was admitted: %d %s", expired.status, expired.body)
	}

	var plain int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM auth_bearers WHERE token_hash IN (?, ?)", secret, short).Scan(&plain); err != nil || plain != 0 {
		t.Fatalf("the database stores a plaintext token: %d, %v", plain, err)
	}
}

func TestAuthTokenRevocationBeatsAWarmOIDCCache(t *testing.T) {
	s := newAPISite(t)
	owner := s.token("owner")
	if me := s.do(http.MethodGet, "/api/v1/me", owner, nil); me.status != http.StatusOK {
		t.Fatalf("first use: %d %s", me.status, me.body)
	}
	if warm := s.do(http.MethodGet, "/api/v1/me", owner, nil); warm.status != http.StatusOK {
		t.Fatalf("cached use: %d %s", warm.status, warm.body)
	}

	listed := runImportApp(t, authTokenApp(t), "auth", "token", "list")
	rows := tokenRows(t, listed.stdout)
	if len(rows) != 1 || rows[0][2] != "oidc" {
		t.Fatalf("the validated JWT is not listed as an enrolled token:\n%s", listed.stdout)
	}
	d := mustConnect(t, filepath.Join(s.dir, "atrium.db"))
	defer func() { _ = d.Close() }()
	var subject string
	if err := d.Read.QueryRow("SELECT subject FROM auth_bearers WHERE id = ?", rows[0][0]).Scan(&subject); err != nil || subject != "sub-owner" {
		t.Fatalf("enrolled subject = %q, %v", subject, err)
	}

	if revoked := runImportApp(t, authTokenApp(t), "auth", "token", "revoke", rows[0][0]); revoked.code != cli.ExitOK {
		t.Fatalf("revoke: exit %d stderr %q", revoked.code, revoked.stderr)
	}
	if refused := s.do(http.MethodGet, "/api/v1/me", owner, nil); refused.status != http.StatusUnauthorized || refused.code() != "invalid_token" {
		t.Fatalf("a revoked JWT was admitted on a warm cache: %d %s", refused.status, refused.body)
	}
	if again := s.do(http.MethodGet, "/api/v1/me", owner, nil); again.status != http.StatusUnauthorized {
		t.Fatalf("the revoked JWT was admitted again: %d %s", again.status, again.body)
	}

	forged := s.token("owner", oidctest.AccessOptions{Foreign: true})
	if bad := s.do(http.MethodGet, "/api/v1/me", forged, nil); bad.status != http.StatusUnauthorized {
		t.Fatalf("a forged JWT was admitted: %d %s", bad.status, bad.body)
	}
	var count int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM auth_bearers").Scan(&count); err != nil || count != 1 {
		t.Fatalf("a failed JWT was enrolled: %d rows, %v", count, err)
	}
}

func TestAuthTokensThroughTheSettingsPage(t *testing.T) {
	s := newAPISite(t)
	browser := oidcBrowser(t, s.base, s.idp, "owner")

	code, body, _ := fetch(t, browser, http.MethodGet, s.base+"/settings/auth", nil)
	if code != http.StatusOK || !strings.Contains(body, "API tokens") || !strings.Contains(body, "No tokens.") {
		t.Fatalf("settings page: %d %.400s", code, body)
	}

	code, body, header := fetch(t, browser, http.MethodPost, s.base+"/settings/auth/tokens", url.Values{"name": {"laptop"}})
	if code != http.StatusOK || header.Get("Cache-Control") != "no-store" {
		t.Fatalf("issue: %d %v %.400s", code, header, body)
	}
	secret := regexp.MustCompile(`atr_[A-Za-z0-9_-]{43}`).FindString(body)
	if secret == "" {
		t.Fatalf("the issue response does not show the secret once: %.400s", body)
	}

	code, body, _ = fetch(t, browser, http.MethodGet, s.base+"/settings/auth", nil)
	if code != http.StatusOK || !strings.Contains(body, "laptop") {
		t.Fatalf("the list does not show the issued token: %d %.400s", code, body)
	}
	if strings.Contains(body, secret) {
		t.Fatalf("the list exposes the secret")
	}
	match := regexp.MustCompile(`/settings/auth/tokens/(\d+)/revoke`).FindStringSubmatch(body)
	if match == nil {
		t.Fatalf("the list has no revoke form: %.400s", body)
	}
	id := match[1]

	if me := s.do(http.MethodGet, "/api/v1/me", secret, nil); me.status != http.StatusOK {
		t.Fatalf("a token issued in the UI cannot read the API: %d %s", me.status, me.body)
	}

	if code, body, _ := fetch(t, browser, http.MethodPost, s.base+"/settings/auth/tokens", url.Values{"name": {" "}}); code != http.StatusOK || !strings.Contains(body, "name") {
		t.Fatalf("a blank name was not refused: %d %.300s", code, body)
	}
	if code, body, _ := fetch(t, browser, http.MethodPost, s.base+"/settings/auth/tokens", url.Values{"name": {"x"}, "expires_at": {"tomorrow"}}); code != http.StatusOK || !strings.Contains(body, "RFC 3339") {
		t.Fatalf("a bad expiry was not refused: %d %.300s", code, body)
	}

	req, err := http.NewRequest(http.MethodPost, s.base+"/settings/auth/tokens", strings.NewReader("name=csrf"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for _, c := range browser.Jar.Cookies(req.URL) {
		req.AddCookie(c)
	}
	resp, err := browser.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a token POST without a CSRF token: %d", resp.StatusCode)
	}

	if code, body, _ := fetch(t, browser, http.MethodPost, s.base+"/settings/auth/tokens/"+id+"/revoke", url.Values{}); code != http.StatusOK {
		t.Fatalf("revoke: %d %.300s", code, body)
	}
	if refused := s.do(http.MethodGet, "/api/v1/me", secret, nil); refused.status != http.StatusUnauthorized || refused.code() != "invalid_token" {
		t.Fatalf("a token revoked in the UI was admitted: %d %s", refused.status, refused.body)
	}
}

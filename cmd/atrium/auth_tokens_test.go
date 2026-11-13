package main

import (
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/auth/bearer"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
)

var tokenHashShape = regexp.MustCompile(`^[0-9a-f]{64}$`)

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
	for bad, want := range map[string]error{" ": bearer.ErrName, strings.Repeat("x", 101): bearer.ErrName} {
		got := runApp(t, "auth", "token", "issue", "--name", bad)
		if !strings.HasSuffix(got.stderr, ": error: "+want.Error()+"\n") || strings.Count(got.stderr, "\n") < 2 {
			t.Errorf("issue --name %q: stderr %q, want the usage and the bearer refusal", bad, got.stderr)
		}
	}
	if got := runApp(t, "auth", "token", "issue", "--name", "old", "--expires-at", time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)); !strings.HasSuffix(got.stderr, ": error: "+bearer.ErrExpiry.Error()+"\n") || strings.Count(got.stderr, "\n") < 2 {
		t.Errorf("issue with a past expiry: stderr %q, want the usage and the bearer refusal", got.stderr)
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

	d := mustConnect(t, filepath.Join(dir, "atrium.db"), db.Key{})
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
	if missing := runApp(t, "auth", "token", "revoke", "999"); missing.code != cli.ExitError || missing.stderr != "error: no token 999\n" {
		t.Fatalf("revoking an unknown id: exit %d stderr %q, want %d and the pre-operation message", missing.code, missing.stderr, cli.ExitError)
	}
	if bad := runApp(t, "auth", "token", "revoke", "x"); bad.code != cli.ExitUsage {
		t.Fatalf("revoking a non-numeric id: exit %d, want usage", bad.code)
	}
	phone = tokenRow(t, runApp(t, "auth", "token", "list").stdout, "phone")
	if phone[6] == "-" {
		t.Fatalf("the revoked token has no revocation time: %v", phone)
	}

	var issued, leaked int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM operation_audit WHERE operation = 'auth.bearer-issue' AND outcome = 'ok' AND target = '1' AND source = 'cli'").Scan(&issued); err != nil {
		t.Fatal(err)
	}
	if issued != 1 {
		t.Fatalf("issuing token 1 left %d operation audit rows, want 1", issued)
	}
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM operation_audit WHERE instr(details_json, 'atr_') > 0 OR instr(details_json, ?) > 0 OR instr(details_json, ?) > 0 OR instr(details_json, ?) > 0",
		secret, strings.TrimSpace(second.stdout), hash).Scan(&leaked); err != nil {
		t.Fatal(err)
	}
	if leaked != 0 {
		t.Fatalf("%d operation audit rows hold token material", leaked)
	}
	outcomes := map[string]int{}
	rows, err := d.Read.Query("SELECT operation || ':' || outcome FROM operation_audit WHERE operation IN ('auth.bearer-issue', 'auth.bearer-revoke')")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			t.Fatal(err)
		}
		outcomes[key]++
	}
	want := map[string]int{"auth.bearer-issue:ok": 2, "auth.bearer-issue:invalid": 6, "auth.bearer-revoke:ok": 1, "auth.bearer-revoke:noop": 1, "auth.bearer-revoke:not_found": 1}
	if !maps.Equal(outcomes, want) {
		t.Fatalf("token operation audit outcomes %v, want %v", outcomes, want)
	}
}

func TestAuthTokensPersistFarFutureExpiry(t *testing.T) {
	moduleFixture(t, flagsExcept("", ""))
	const expiry = "2300-01-01T00:00:00Z"
	issued := runApp(t, "auth", "token", "issue", "--name", "long-lived", "--expires-at", expiry)
	if issued.code != cli.ExitOK {
		t.Fatalf("issue: exit %d stderr %q", issued.code, issued.stderr)
	}
	secret := strings.TrimSpace(issued.stdout)
	base, _, stop, err := startServeAppArgs(t, fixtureFor(t).app, &lockedBuffer{}, "--auth", "local")
	if err != nil {
		t.Fatal(err)
	}
	s := &apiSite{t: t, base: base}
	if me := s.do(http.MethodGet, "/api/v1/me", secret, nil); me.status != http.StatusOK {
		t.Fatalf("a long-lived token cannot read the API: %d %s", me.status, me.body)
	}
	stop()
	row := tokenRow(t, runApp(t, "auth", "token", "list").stdout, "long-lived")
	if row[4] != expiry {
		t.Fatalf("the long-lived expiry is listed as %q, want %q", row[4], expiry)
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

	if code, body, _ := fetch(t, browser, http.MethodPost, s.base+"/settings/auth/tokens", url.Values{"name": {" "}}); code != http.StatusUnprocessableEntity || !strings.Contains(body, "name") {
		t.Fatalf("a blank name was not refused: %d %.300s", code, body)
	}
	if code, body, _ := fetch(t, browser, http.MethodPost, s.base+"/settings/auth/tokens", url.Values{"name": {"x"}, "expires_at": {"tomorrow"}}); code != http.StatusUnprocessableEntity || !strings.Contains(body, "RFC 3339") {
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

	d := mustConnect(t, filepath.Join(s.dir, "atrium.db"), db.Key{})
	var issuedRow, revokedRow int64
	if err := d.Read.QueryRow("SELECT id FROM settings_audit WHERE key = 'auth_token.issue' AND new_value LIKE 'id=' || ? || ' %' AND source = 'ui'", id).Scan(&issuedRow); err != nil {
		t.Fatalf("no settings history row for the issue: %v", err)
	}
	if err := d.Read.QueryRow("SELECT id FROM settings_audit WHERE key = 'auth_token.revoke' AND new_value LIKE 'id=' || ? || ' %' AND source = 'ui'", id).Scan(&revokedRow); err != nil {
		t.Fatalf("no settings history row for the revoke: %v", err)
	}
	var leaked int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM settings_audit WHERE instr(COALESCE(new_value, ''), 'atr_') > 0 OR instr(COALESCE(old_value, ''), 'atr_') > 0").Scan(&leaked); err != nil || leaked != 0 {
		t.Fatalf("settings history holds token material: %d, %v", leaked, err)
	}
	code, body, _ = fetch(t, browser, http.MethodGet, s.base+"/activity?category=settings", nil)
	if code != http.StatusOK {
		t.Fatalf("activity: %d", code)
	}
	for _, row := range []int64{issuedRow, revokedRow} {
		if !strings.Contains(body, fmt.Sprintf(`id="activity-settings-%d"`, row)) {
			t.Errorf("activity does not list settings history row %d", row)
		}
	}
	if strings.Contains(body, secret) || strings.Contains(body, "atr_") {
		t.Errorf("activity shows token material")
	}
}

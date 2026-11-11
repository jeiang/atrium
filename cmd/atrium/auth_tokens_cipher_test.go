//go:build !modernc

package main

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/auth/oidc/oidctest"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/mail/imapclient"
	"github.com/jeiang/atrium/internal/settings"
)

func authTokenApp(t *testing.T) *cli.App {
	t.Helper()
	f := fixtureFor(t)
	if f == nil {
		t.Fatal("no fixture")
	}
	reg := settings.New()
	return newAppWith(f.lookup, reg, config.NewState(reg), nil, imapclient.Opener, nil, quietLog)
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

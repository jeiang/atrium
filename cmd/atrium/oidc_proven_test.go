package main

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWimOIDCKeepsAProvenClientAuthAcrossTestsUntilItsSecretChanges(t *testing.T) {
	s := wimOIDCSite(t)
	b := s.browser()
	b.mustSignIn("admin")
	row := func() (state, clientAuth string) {
		t.Helper()
		if err := s.db.Read.QueryRow(`SELECT c.state, json_extract(r.value, '$.state') FROM connections c, json_each(c.results_json) r
			WHERE c.id = 'service/oidc' AND json_extract(r.value, '$.capability') = 'client_auth'`).Scan(&state, &clientAuth); err != nil {
			t.Fatalf("service/oidc row: %v", err)
		}
		return state, clientAuth
	}
	expect := func(when, wantState, wantClientAuth string) {
		t.Helper()
		if state, clientAuth := row(); state != wantState || clientAuth != wantClientAuth {
			t.Fatalf("%s: service/oidc %s with client_auth %s, want %s with %s", when, state, clientAuth, wantState, wantClientAuth)
		}
	}
	test := func() {
		t.Helper()
		got := b.do(http.MethodPost, "/web/v1/connections/service/oidc/test", nil,
			"Atrium-Client", "web", "Origin", s.base, "X-CSRF-Token", b.cookie("atrium_csrf"))
		if got.status != http.StatusOK {
			t.Fatalf("POST /web/v1/connections/service/oidc/test: %d %s", got.status, got.body)
		}
	}
	secret := func(value string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(s.dir, "oidc-client-secret"), []byte(value+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	expect("after a sign-in", "ok", "ok")
	test()
	expect("after a Test", "ok", "ok")
	if got := runApp(t, "connections", "test", "--all"); !strings.Contains(got.stdout, "service/oidc\tok") {
		t.Fatalf("connections test --all: exit %d stdout %q stderr %q", got.code, got.stdout, got.stderr)
	}
	expect("after a silent health pass", "ok", "ok")

	secret("a-different-secret")
	test()
	expect("after the client secret changed", "untested", "untested")

	_ = s.browser().signIn("admin")
	expect("after a sign-in the provider refused", "failing", "failing")
	test()
	expect("after a Test that follows a refused sign-in", "failing", "failing")

	secret("s3cret-for-the-test")
	test()
	expect("after the secret was restored", "untested", "untested")
	s.browser().mustSignIn("admin")
	expect("after a new sign-in", "ok", "ok")
}

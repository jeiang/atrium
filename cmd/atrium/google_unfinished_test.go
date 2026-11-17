package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAnAbandonedGoogleWizardIsUnconfiguredWhileAWorkingGoogleThatLosesItsClientFails(t *testing.T) {
	fake := &googleTokenFake{status: http.StatusOK, refresh: "refresh-1"}
	srv := httptest.NewServer(http.HandlerFunc(fake.handler))
	t.Cleanup(srv.Close)
	requireCipher(t)
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	setEnv(t, "ATRIUM_DB_KEY", rightKeyHex)
	t.Setenv("ATRIUM_GOOGLE_AUTH_URL", srv.URL+"/auth")
	t.Setenv("ATRIUM_GOOGLE_TOKEN_URL", srv.URL+"/token")
	t.Setenv("ATRIUM_GOOGLE_API_URL", srv.URL+"/calendar/v3")
	t.Setenv("ATRIUM_CALENDAR_GOOGLE_API_URL", srv.URL+"/calendar/v3")
	ops := opsAddr(t)
	base, _, stop := startServeStoppable(t, dir)
	browser := localWebG5(t, base)
	test := func() apiReply {
		t.Helper()
		return browser.do(http.MethodPost, "/web/v1/connections/google/default/test", nil)
	}
	d := keyedDB(t, filepath.Join(dir, "atrium.db"))
	reason := func() string {
		return queryValue(t, d.Read, "SELECT reason FROM connections WHERE id = 'google/default'")
	}
	metrics := func() string {
		code, body, _ := fetch(t, http.DefaultClient, http.MethodGet, ops()+"/metrics", nil)
		if code != http.StatusOK {
			t.Fatalf("GET /metrics = %d", code)
		}
		return body
	}
	unconfigured := func(when string) {
		t.Helper()
		if got := googleHealth(t, d); got != "unconfigured" {
			t.Fatalf("%s: google/default = %q (%s), want unconfigured", when, got, reason())
		}
		if n := connectionNotices(t, dir, "Google"); n != 0 {
			t.Fatalf("%s: %d Google notices, want 0", when, n)
		}
		body := metrics()
		if strings.Contains(body, `atrium_integration_state{integration="google/default",state="failing"} 1`) ||
			!strings.Contains(body, `atrium_integration_state{integration="google/default",state="unconfigured"} 1`) {
			t.Fatalf("%s: metrics:\n%s", when, body)
		}
	}

	if opened := browser.do(http.MethodGet, "/web/v1/connections/google/oauth", nil); opened.status != http.StatusOK {
		t.Fatalf("google wizard: %d %s", opened.status, opened.body)
	}
	test()
	unconfigured("an abandoned wizard")
	if got := reason(); got != "google client ID is not set" {
		t.Fatalf("abandoned wizard reason = %q", got)
	}

	stop()
	if _, err := d.Write.Exec("UPDATE connections SET state = 'failing', reason = 'google client ID is not set', checked_at = ? WHERE id = 'google/default'",
		float64(time.Now().Add(-time.Hour).Unix())); err != nil {
		t.Fatal(err)
	}
	base, _, stop = startServeStoppable(t, dir)
	t.Cleanup(stop)
	browser = localWebG5(t, base)
	settleBootChecks(t, dir, keyFor(t))
	unconfigured("a failing row from an earlier release after a restart")

	if saved := browser.do(http.MethodPut, "/web/v1/connections/google/oauth", map[string]any{"client_id": "client-id", "client_secret": "secret"}); saved.status != http.StatusOK {
		t.Fatalf("save client: %d %s", saved.status, saved.body)
	}
	test()
	unconfigured("a client ID without consent")

	started := browser.do(http.MethodPost, "/web/v1/connections/google/oauth/start", map[string]any{"capability": "calendar"})
	data, _ := started.json["data"].(map[string]any)
	raw, _ := data["authorize_url"].(string)
	if started.status != http.StatusOK || raw == "" {
		t.Fatalf("start calendar: %d %s", started.status, started.body)
	}
	consent, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if done := browser.do(http.MethodPost, "/web/v1/connections/google/oauth/complete", map[string]any{"state": consent.Query().Get("state"), "code": "code-1"}); done.status != http.StatusOK {
		t.Fatalf("calendar consent: %d %s", done.status, done.body)
	}
	if passed := test(); passed.status != http.StatusOK {
		t.Fatalf("passing test: %d %s", passed.status, passed.body)
	}
	if got := googleHealth(t, d); got != "ok" {
		t.Fatalf("google/default after calendar consent = %q (%s), want ok", got, reason())
	}

	if _, err := d.Write.Exec(`UPDATE connections SET config_json = json_remove(config_json, '$.client_id') WHERE id = 'google/default'`); err != nil {
		t.Fatal(err)
	}
	test()
	if got := googleHealth(t, d); got != "failing" {
		t.Fatalf("google/default after losing its client ID = %q (%s), want failing", got, reason())
	}
	waitFor(t, func() bool { return connectionNotices(t, dir, "Google") > 0 })
	test()
	if n := connectionNotices(t, dir, "Google"); n != 1 {
		t.Fatalf("%d Google notices after the loss, want 1", n)
	}
}

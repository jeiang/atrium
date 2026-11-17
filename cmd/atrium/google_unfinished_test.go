package main

import (
	"net/http"
	"net/http/cookiejar"
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
	base, logs, stop := startServeStoppable(t, dir)
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	noRedirect := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	d := keyedDB(t, filepath.Join(dir, "atrium.db"))
	reason := func() string {
		return queryValue(t, d.Read, "SELECT reason FROM connections WHERE id = 'google/default'")
	}
	metrics := func() string {
		code, body, _ := fetch(t, http.DefaultClient, http.MethodGet, ops+"/metrics", nil)
		if code != http.StatusOK {
			t.Fatalf("GET /metrics = %d", code)
		}
		return body
	}
	// The ops endpoint caches a scrape for 15 seconds; a second read in one serve process may see the cached body.
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

	// The operator opens the wizard and leaves before saving a client ID; the next check runs.
	if code, _, _ := fetch(t, client, http.MethodGet, base+"/settings/connections/google", nil); code != http.StatusOK {
		t.Fatalf("google page: %d %s", code, logs.String())
	}
	_, _, _ = fetch(t, client, http.MethodPost, base+"/settings/connections/google/test", url.Values{})
	unconfigured("an abandoned wizard")
	if got := reason(); got != "google client ID is not set" {
		t.Fatalf("abandoned wizard reason = %q", got)
	}

	// A row an earlier release left failing becomes unconfigured on the next start check, without a notice.
	stop()
	if _, err := d.Write.Exec("UPDATE connections SET state = 'failing', reason = 'google client ID is not set', checked_at = ? WHERE id = 'google/default'",
		float64(time.Now().Add(-time.Hour).Unix())); err != nil {
		t.Fatal(err)
	}
	base, logs, stop = startServeStoppable(t, dir)
	t.Cleanup(stop)
	settleBootChecks(t, dir, keyFor(t))
	unconfigured("a failing row from an earlier release after a restart")

	// A client ID saved without consent is still unfinished.
	if code, _, _ := fetch(t, noRedirect, http.MethodPost, base+"/settings/connections/google",
		url.Values{"client_id": {"client-id"}, "client_secret": {"secret"}}); code != http.StatusSeeOther {
		t.Fatalf("save client: %d %s", code, logs.String())
	}
	_, _, _ = fetch(t, client, http.MethodPost, base+"/settings/connections/google/test", url.Values{})
	unconfigured("a client ID without consent")

	// Consent for one capability makes it work; the capability never consented is not a failure.
	code, _, hdr := fetch(t, noRedirect, http.MethodPost, base+"/settings/connections/google/start", url.Values{"capability": {"calendar"}})
	if code != http.StatusSeeOther {
		t.Fatalf("start calendar: %d", code)
	}
	consent, err := url.Parse(hdr.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if code, _, _ := fetch(t, noRedirect, http.MethodGet, base+"/settings/connections/google/callback?state="+
		url.QueryEscape(consent.Query().Get("state"))+"&code=code-1", nil); code != http.StatusSeeOther {
		t.Fatalf("calendar callback: %d", code)
	}
	if code, body, _ := fetch(t, client, http.MethodPost, base+"/settings/connections/google/test", url.Values{}); code != http.StatusOK {
		t.Fatalf("passing test: %d %s", code, body)
	}
	if got := googleHealth(t, d); got != "ok" {
		t.Fatalf("google/default after calendar consent = %q (%s), want ok", got, reason())
	}

	// The working connection loses its client ID: that broke something, so it fails and notifies once.
	if _, err := d.Write.Exec(`UPDATE connections SET config_json = json_remove(config_json, '$.client_id') WHERE id = 'google/default'`); err != nil {
		t.Fatal(err)
	}
	_, _, _ = fetch(t, client, http.MethodPost, base+"/settings/connections/google/test", url.Values{})
	if got := googleHealth(t, d); got != "failing" {
		t.Fatalf("google/default after losing its client ID = %q (%s), want failing", got, reason())
	}
	waitFor(t, func() bool { return connectionNotices(t, dir, "Google") > 0 })
	_, _, _ = fetch(t, client, http.MethodPost, base+"/settings/connections/google/test", url.Values{})
	if n := connectionNotices(t, dir, "Google"); n != 1 {
		t.Fatalf("%d Google notices after the loss, want 1", n)
	}
}

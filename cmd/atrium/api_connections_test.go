//go:build !modernc

package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestConnectionsAndTokensAPI(t *testing.T) {
	requireCipher(t)
	grafana, grafanaStatus := grafanaHealthServer(t)
	google := &googleTokenFake{status: http.StatusOK, refresh: "refresh-api"}
	gsrv := httptest.NewServer(http.HandlerFunc(google.handler))
	t.Cleanup(gsrv.Close)
	_, _, f := moduleFixture(t, flagsExcept("", ""))
	setEnv(t, "ATRIUM_DB_KEY", rightKeyHex)
	setEnv(t, "ATRIUM_HOST_CONFIG", servicesHostConfig(t, f.dir, grafana.URL, "http://anthropic.invalid"))
	t.Setenv("ATRIUM_GOOGLE_AUTH_URL", gsrv.URL+"/auth")
	t.Setenv("ATRIUM_GOOGLE_TOKEN_URL", gsrv.URL+"/token")
	t.Setenv("ATRIUM_GOOGLE_API_URL", gsrv.URL+"/calendar/v3")
	token := localAPIToken(t, "api")
	base, _, stop, err := startServeAppArgs(t, f.app, &lockedBuffer{}, "--auth", "local")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	s := &apiSite{t: t, base: base}
	d := keyedDB(t, filepath.Join(f.dir, "atrium.db"))
	stored := func(value string) string {
		return importValue(t, d.Read, "SELECT COUNT(*) FROM connection_credentials WHERE connection_id = 'service/grafana' AND value = ?", value)
	}

	grafanaStatus.Store(http.StatusUnauthorized)
	if bad := s.do(http.MethodPut, "/api/v1/connections/service/grafana", token, map[string]any{"credentials": map[string]string{"api_key": "glsa_bad"}}); bad.status < 400 || strings.Contains(bad.body, "glsa_bad") {
		t.Fatalf("test-save with a bad credential: %d %s", bad.status, bad.body)
	}
	if n := stored("glsa_bad"); n != "0" {
		t.Fatalf("a refused credential was stored: %s", n)
	}
	grafanaStatus.Store(http.StatusOK)
	saved := s.do(http.MethodPut, "/api/v1/connections/service/grafana", token, map[string]any{"credentials": map[string]string{"api_key": "glsa_good"}})
	if got := apiData(t, saved, http.StatusOK); got["state"] != "ok" || strings.Contains(saved.body, "glsa_good") {
		t.Fatalf("test-save with a good credential: %s", saved.body)
	}
	if state := connectionState(t, f.dir, "service/grafana"); state != "ok" {
		t.Fatalf("grafana state = %q", state)
	}
	listed := s.do(http.MethodGet, "/api/v1/connections", token, nil)
	if apiData(t, listed, http.StatusOK)["connections"] == nil || strings.Contains(listed.body, "glsa_good") {
		t.Fatalf("connections list: %.300s", listed.body)
	}
	if got := s.do(http.MethodDelete, "/api/v1/connections/service/grafana", token, nil); got.status != http.StatusNoContent {
		t.Fatalf("disconnect: %d %s", got.status, got.body)
	}
	for _, c := range apiData(t, s.do(http.MethodGet, "/api/v1/connections", token, nil), http.StatusOK)["connections"].([]any) {
		if c := c.(map[string]any); c["id"] == "service/grafana" && c["enabled"] != false {
			t.Fatalf("grafana after disconnect: %v", c)
		}
	}

	apiData(t, s.do(http.MethodPut, "/api/v1/connections/google/oauth", token, map[string]any{"client_id": "client-id", "client_secret": "client-secret"}), http.StatusOK)
	started := apiData(t, s.do(http.MethodPost, "/api/v1/connections/google/oauth/start", token, map[string]any{"capability": "calendar"}), http.StatusOK)
	state, _ := started["state"].(string)
	if state == "" || !strings.Contains(started["authorize_url"].(string), "code_challenge_method=S256") || !strings.Contains(started["authorize_url"].(string), "redirect_uri="+url.QueryEscape(base+"/settings/connections/google/callback")) {
		t.Fatalf("oauth start: %v", started)
	}
	expectAPI(t, s.do(http.MethodPost, "/api/v1/connections/google/oauth/start", token, map[string]any{"capability": "calendar", "redirect_uri": "https://evil.example/cb"}), http.StatusBadRequest, "bad_request")
	if got := apiData(t, s.do(http.MethodPost, "/api/v1/connections/google/oauth/complete", token, map[string]any{"state": state, "code": "code-1"}), http.StatusOK); got["name"] != "calendar" {
		t.Fatalf("oauth complete: %v", got)
	}
	if refresh := importValue(t, d.Read, "SELECT value FROM connection_credentials WHERE connection_id = 'google/default' AND slot = 'oauth/calendar/refresh'"); refresh != "refresh-api" {
		t.Fatalf("calendar refresh token = %q", refresh)
	}
	if reused := s.do(http.MethodPost, "/api/v1/connections/google/oauth/complete", token, map[string]any{"state": state, "code": "code-2"}); reused.status < 400 || reused.status >= 500 {
		t.Fatalf("reusing an oauth state: %d %s", reused.status, reused.body)
	}

	issue := func() apiReply {
		return s.do(http.MethodPost, "/api/v1/auth/tokens", token, map[string]any{"name": "second"}, "Idempotency-Key", "issue-1")
	}
	issued := apiData(t, issue(), http.StatusCreated)
	secret, _ := issued["secret"].(string)
	id := int64(number(t, issued["token"].(map[string]any)["id"]))
	if secret == "" {
		t.Fatalf("issued token: %v", issued)
	}
	expectAPI(t, issue(), http.StatusConflict, "command_result_withheld")
	if list := s.do(http.MethodGet, "/api/v1/auth/tokens", secret, nil); list.status != http.StatusOK || strings.Contains(list.body, secret) {
		t.Fatalf("token list: %d %.300s", list.status, list.body)
	}
	if got := s.do(http.MethodDelete, "/api/v1/auth/tokens/"+strconv.FormatInt(id, 10), token, nil); got.status != http.StatusNoContent {
		t.Fatalf("revoke: %d %s", got.status, got.body)
	}
	if got := s.do(http.MethodGet, "/api/v1/auth/tokens", secret, nil); got.status != http.StatusUnauthorized {
		t.Fatalf("a revoked bearer: %d", got.status)
	}
	expectConditional(t, s, token, "/api/v1/auth/tokens")

	hour := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	short := apiData(t, s.do(http.MethodPost, "/api/v1/auth/tokens", token, map[string]any{"name": "short", "expires_at": hour.Format(time.RFC3339)}), http.StatusCreated)
	shortSecret, _ := short["secret"].(string)
	expectAPI(t, s.do(http.MethodPost, "/api/v1/auth/tokens", shortSecret, map[string]any{"name": "forever"}), http.StatusUnprocessableEntity, "invalid")
	expectAPI(t, s.do(http.MethodPost, "/api/v1/auth/tokens", shortSecret, map[string]any{"name": "longer", "expires_at": hour.Add(time.Hour).Format(time.RFC3339)}), http.StatusUnprocessableEntity, "invalid")
	child := apiData(t, s.do(http.MethodPost, "/api/v1/auth/tokens", shortSecret, map[string]any{"name": "child", "expires_at": hour.Add(-time.Minute).Format(time.RFC3339)}), http.StatusCreated)
	if exp, _ := child["token"].(map[string]any)["expires_at"].(string); exp == "" {
		t.Fatalf("a token minted by a short-lived bearer has no expiry: %v", child)
	}
	var minted int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM auth_bearers WHERE name IN ('forever', 'longer')").Scan(&minted); err != nil || minted != 0 {
		t.Fatalf("refused tokens were stored: %d %v", minted, err)
	}

}

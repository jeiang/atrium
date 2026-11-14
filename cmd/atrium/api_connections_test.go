//go:build !modernc

package main

import (
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/cli"
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
	patched := s.do(http.MethodPatch, "/api/v1/connections/service/grafana", token, map[string]any{"label": "Ops Grafana", "credentials": map[string]string{"api_key": "glsa_patch"}})
	if got := apiData(t, patched, http.StatusOK); got["label"] != "Ops Grafana" || got["state"] != "untested" || strings.Contains(patched.body, "glsa_patch") {
		t.Fatalf("save fields: %s", patched.body)
	}
	if n := stored("glsa_patch"); n != "1" {
		t.Fatalf("saved credential rows = %s, want 1", n)
	}
	expectAPI(t, s.do(http.MethodPatch, "/api/v1/connections/service/grafana", token, map[string]any{"colour": "red"}), http.StatusBadRequest, "bad_request")
	expectAPI(t, s.do(http.MethodPatch, "/api/v1/connections/service/missing", token, map[string]any{"label": "x"}), http.StatusNotFound, "not_found")
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
	if fresh := s.do(http.MethodGet, "/api/v1/auth/tokens", token, nil, "If-None-Match", "*"); fresh.status != http.StatusOK || fresh.header.Get("ETag") != "" {
		t.Fatalf("token list answered a conditional read: %d %v", fresh.status, fresh.header)
	}

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

func TestConnectionCredentialCannotFollowAnEndpointChange(t *testing.T) {
	requireCipher(t)
	setEnv(t, "ATRIUM_DB_KEY", rightKeyHex)
	dir, _, f := moduleFixture(t, flagsExcept("", ""))
	setEnv(t, "ATRIUM_HOST_CONFIG", writeFile(t, dir, "operator.toml", ""))
	var mu sync.Mutex
	var seenA, seenB []string
	endpoint := func(seen *[]string) *httptest.Server {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			*seen = append(*seen, r.Header.Get("Authorization"))
			mu.Unlock()
			switch r.URL.Path {
			case "/api/user":
				_, _ = w.Write([]byte(`{"login":"atrium"}`))
			case "/api/search":
				_, _ = w.Write([]byte(`[]`))
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		t.Cleanup(server.Close)
		return server
	}
	a, b := endpoint(&seenA), endpoint(&seenB)
	d := keyedDB(t, filepath.Join(dir, "atrium.db"))
	if _, err := d.Write.Exec("DELETE FROM connections WHERE id = 'service/grafana'"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.Exec("INSERT INTO connections (id, kind, label, config_source, config_json, transport_json, revision, enabled, state, created_at, updated_at) VALUES ('service/grafana', 'service/grafana', 'Grafana', 'operator', ?, '{}', 1, 1, 'untested', 0, 0)", fmt.Sprintf(`{"url":%q}`, a.URL)); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.Exec("INSERT INTO connection_capabilities (connection_id, name, enabled, config_json) VALUES ('service/grafana', 'viewer', 1, '{}')"); err != nil {
		t.Fatal(err)
	}
	token := localAPIToken(t, "credential-move")
	base, _, stop, err := startServeAppArgs(t, f.app, &lockedBuffer{}, "--auth", "local")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	s := &apiSite{t: t, base: base}
	const path = "/api/v1/connections/service/grafana"
	userinfoURL, err := url.Parse(b.URL)
	if err != nil {
		t.Fatal(err)
	}
	const userinfoPassword = "endpoint-userinfo-must-not-be-audited"
	userinfoURL.User = url.UserPassword("operator", userinfoPassword)
	for _, method := range []string{http.MethodPatch, http.MethodPut} {
		reply := s.do(method, path, token, map[string]any{"url": userinfoURL.String()})
		expectAPI(t, reply, http.StatusUnprocessableEntity, "invalid")
		if strings.Contains(reply.body, userinfoPassword) {
			t.Fatal("userinfo password was returned")
		}
		if !strings.Contains(reply.body, "url") || !strings.Contains(reply.body, "userinfo") {
			t.Fatal("userinfo refusal does not name the field and cause")
		}
	}
	old := map[string]string{"viewer_token": "glsa_old_viewer", "api_key": "glsa_old_fallback"}
	apiData(t, s.do(http.MethodPut, path, token, map[string]any{"url": a.URL, "credentials": old}), http.StatusOK)
	apiData(t, s.do(http.MethodPatch, path, token, map[string]any{"url": a.URL}), http.StatusOK)
	refuse := func(reply apiReply) {
		t.Helper()
		expectAPI(t, reply, http.StatusUnprocessableEntity, "invalid")
		for _, secret := range old {
			if strings.Contains(reply.body, secret) {
				t.Fatalf("credential in refusal: %s", reply.body)
			}
		}
		if !strings.Contains(reply.body, "replace every retained credential") || !strings.Contains(reply.body, "disconnect") {
			t.Fatalf("credential move refusal lacks a remedy: %s", reply.body)
		}
	}
	refuse(s.do(http.MethodPatch, path, token, map[string]any{"url": b.URL}))
	refuse(s.do(http.MethodPut, path, token, map[string]any{"url": b.URL}))
	refuse(s.do(http.MethodPut, path, token, map[string]any{"url": b.URL, "credentials": map[string]string{"api_key": "glsa_new_fallback"}}))
	expectAPI(t, s.do(http.MethodPatch, path, token, map[string]any{"credentials": map[string]string{"unexpected": "never-store-me"}}), http.StatusUnprocessableEntity, "invalid")
	expectAPI(t, s.do(http.MethodPatch, path, token, map[string]any{"credentials": map[string]string{"unexpected": ""}}), http.StatusUnprocessableEntity, "invalid")
	apiData(t, s.do(http.MethodPatch, path, token, map[string]any{"url": a.URL, "credentials": map[string]string{"viewer_token": ""}}), http.StatusOK)
	if count := importValue(t, d.Read, "SELECT COUNT(*) FROM connection_credentials WHERE connection_id = 'service/grafana' AND slot = 'unexpected'"); count != "0" {
		t.Fatalf("unknown slot was stored: %s", count)
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	browser := &http.Client{Jar: jar}
	if status, _, _ := fetch(t, browser, http.MethodGet, base+"/settings/connections", nil); status != http.StatusOK {
		t.Fatalf("connections page: %d", status)
	}
	if status, body, _ := fetch(t, browser, http.MethodPost, base+"/settings/connections/test", url.Values{"id": {"service/grafana"}, "url": {b.URL}}); status != http.StatusUnprocessableEntity || strings.Contains(body, old["viewer_token"]) {
		t.Fatalf("HTML endpoint change: %d %s", status, body)
	}
	if status, body, _ := fetch(t, browser, http.MethodPost, base+"/settings/connections/test", url.Values{"id": {"service/grafana"}, "url": {userinfoURL.String()}}); status != http.StatusUnprocessableEntity || strings.Contains(body, userinfoPassword) {
		t.Fatalf("HTML userinfo refusal: status %d; password absent %t", status, !strings.Contains(body, userinfoPassword))
	}
	if count := importValue(t, d.Read, "SELECT COUNT(*) FROM settings_audit WHERE instr(COALESCE(old_value, '') || COALESCE(new_value, ''), ?) > 0", userinfoPassword); count != "0" {
		t.Fatal("userinfo password entered the settings audit")
	}
	mu.Lock()
	requestsAtB := append([]string(nil), seenB...)
	mu.Unlock()
	if len(requestsAtB) != 0 {
		t.Fatalf("refused endpoint B received requests with Authorization %q", requestsAtB)
	}
	if source, _, _, cfg := connectionRow(t, dir, "service/grafana"); source != "operator" || !strings.Contains(cfg, a.URL) {
		t.Fatalf("refused move changed the connection: %s %s", source, cfg)
	}
	for slot, secret := range old {
		if got := importValue(t, d.Read, "SELECT value FROM connection_credentials WHERE connection_id = 'service/grafana' AND slot = ?", slot); got != secret {
			t.Fatalf("refused move changed slot %s", slot)
		}
	}
	replacement := map[string]string{"viewer_token": "glsa_new_viewer", "api_key": "glsa_new_fallback"}
	patched := s.do(http.MethodPatch, path, token, map[string]any{"url": b.URL, "credentials": replacement})
	if got := apiData(t, patched, http.StatusOK); got["state"] != "untested" {
		t.Fatalf("replacement PATCH: %s", patched.body)
	}
	tested := s.do(http.MethodPut, path, token, map[string]any{"url": b.URL})
	if got := apiData(t, tested, http.StatusOK); got["state"] != "ok" {
		t.Fatalf("test after replacement: %s", tested.body)
	}
	mu.Lock()
	requestsAtB = append([]string(nil), seenB...)
	mu.Unlock()
	if len(requestsAtB) < 2 {
		t.Fatalf("replacement did not test endpoint B: %q", requestsAtB)
	}
	for _, authorization := range requestsAtB {
		if authorization != "Bearer "+replacement["viewer_token"] {
			t.Fatalf("endpoint B received Authorization %q", authorization)
		}
	}
	apiData(t, s.do(http.MethodPut, path, token, map[string]any{"url": a.URL, "credentials": old}), http.StatusOK)
	apiData(t, s.do(http.MethodPut, path, token, map[string]any{"url": b.URL, "credentials": replacement}), http.StatusOK)
	mu.Lock()
	requestsAtB = append([]string(nil), seenB...)
	mu.Unlock()
	for _, authorization := range requestsAtB {
		if authorization != "Bearer "+replacement["viewer_token"] {
			t.Fatalf("endpoint B received an old Authorization after PUT: %q", authorization)
		}
	}
	for _, reply := range []apiReply{patched, tested, s.do(http.MethodGet, "/api/v1/connections", token, nil)} {
		for _, secret := range replacement {
			if strings.Contains(reply.body, secret) {
				t.Fatalf("credential in response: %s", reply.body)
			}
		}
	}
	if got := s.do(http.MethodDelete, path, token, nil); got.status != http.StatusNoContent {
		t.Fatalf("disconnect: %d %s", got.status, got.body)
	}
	apiData(t, s.do(http.MethodPatch, path, token, map[string]any{"url": a.URL}), http.StatusOK)
	if count := importValue(t, d.Read, "SELECT COUNT(*) FROM connection_credentials WHERE connection_id = 'service/grafana'"); count != "0" {
		t.Fatalf("disconnect retained %s credentials", count)
	}
}

func TestConnectionOIDCIssuerAndPresetCannotMoveARetainedClientSecret(t *testing.T) {
	requireCipher(t)
	setEnv(t, "ATRIUM_DB_KEY", rightKeyHex)
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	setEnv(t, "ATRIUM_HOST_CONFIG", writeFile(t, dir, "operator-oidc.toml", ""))
	d := keyedDB(t, filepath.Join(dir, "atrium.db"))
	const original = `{"issuer":"https://issuer.example/original","preset":"generic","client_id":"atrium"}`
	if _, err := d.Write.Exec("INSERT INTO connections (id, kind, label, config_source, config_json, transport_json, revision, enabled, state, created_at, updated_at) VALUES ('service/oidc', 'service/oidc', 'Sign-in', 'operator', ?, '{}', 1, 1, 'untested', 0, 0) ON CONFLICT(id) DO UPDATE SET config_source = 'operator', config_json = excluded.config_json", original); err != nil {
		t.Fatal(err)
	}
	const secret = "oidc-client-secret-retained-at-original"
	if _, err := d.Write.Exec("INSERT INTO connection_credentials (connection_id, slot, source, value, set_at, set_by) VALUES ('service/oidc', 'client_secret', 'database', ?, 0, 'fixture')", secret); err != nil {
		t.Fatal(err)
	}
	for _, change := range []struct{ field, issuer, preset string }{
		{"issuer", "https://issuer.example/changed", "generic"},
		{"preset", "https://issuer.example/original", "keycloak"},
	} {
		t.Run(change.field, func(t *testing.T) {
			host := fmt.Sprintf("[connections.\"service/oidc\"]\nkind = \"service/oidc\"\n[connections.\"service/oidc\".config]\nissuer = %q\npreset = %q\nclient_id = \"atrium\"\n", change.issuer, change.preset)
			setEnv(t, "ATRIUM_HOST_CONFIG", writeFile(t, dir, change.field+".toml", host))
			reply := runApp(t, "connections", "list")
			output := reply.stdout + reply.stderr
			if reply.code == 0 || !strings.Contains(output, "service/oidc") || !strings.Contains(output, change.field) || !strings.Contains(output, "disconnect") {
				t.Fatal("CLI host takeover did not refuse the identity change with a field-specific remedy")
			}
			if strings.Contains(output, secret) {
				t.Fatal("CLI identity refusal returned the client secret")
			}
			if cfg := importValue(t, d.Read, "SELECT config_json FROM connections WHERE id = 'service/oidc'"); cfg != original {
				t.Fatal("refused OIDC takeover changed the stored configuration")
			}
			if value := importValue(t, d.Read, "SELECT value FROM connection_credentials WHERE connection_id = 'service/oidc' AND slot = 'client_secret'"); value != secret {
				t.Fatal("refused OIDC takeover changed the client secret")
			}
		})
	}
}

func TestConnectionHostEndpointRefusalStopsServeAsAConfigurationError(t *testing.T) {
	requireCipher(t)
	setEnv(t, "ATRIUM_DB_KEY", rightKeyHex)
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	const password = "host-userinfo-must-not-be-stored"
	passwordFile := writeFile(t, dir, "x.password", "mail pass")
	mail := func(field, value string) string {
		return "\n[connections.\"mail/x\"]\nkind = \"mail\"\nconfig = { preset = \"icloud\", login = \"me@icloud.test\", auth_method = \"password\", " + field + " = " + strconv.Quote(value) + " }\ncapabilities = [\"imap\", \"smtp\"]\ncredential_files = { \"mail/x/password\" = " + strconv.Quote(passwordFile) + " }\n"
	}
	d := keyedDB(t, filepath.Join(dir, "atrium.db"))
	before := importValue(t, d.Read, "SELECT COUNT(*) FROM connections")
	for _, tc := range []struct{ name, id, field, body string }{
		{"issuer userinfo", "service/oidc", "issuer", "\n[connections.\"service/oidc\"]\nkind = \"service/oidc\"\n[connections.\"service/oidc\".config]\nissuer = \"https://operator:" + password + "@issuer.example/\"\nclient_id = \"atrium\"\n"},
		{"url userinfo", "service/qbittorrent", "url", strings.Replace(defaultQbitConnection, "http://qbittorrent.invalid", "http://operator:"+password+"@qbittorrent.invalid", 1)},
		{"scheme-less host userinfo", "mail/x", "imap_host", mail("imap_host", "operator:"+password+"@imap.example.com")},
		{"host with a path", "mail/x", "smtp_host", mail("smtp_host", "smtp.example.com/"+password)},
		{"host with whitespace", "mail/x", "imap_host", mail("imap_host", "imap.example.com "+password)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setEnv(t, "ATRIUM_HOST_CONFIG", writeFile(t, dir, "refused.toml", hostConfigBody(defaultWebPushConnection)+tc.body))
			reply := runApp(t, "serve", "--port", "0", "--auth", "local")
			output := reply.stdout + reply.stderr
			if reply.code != cli.ExitConfig || !strings.Contains(output, tc.id) || !strings.Contains(output, tc.field) {
				t.Fatalf("serve exit %d, output %q; want %d naming %s and %s", reply.code, output, cli.ExitConfig, tc.id, tc.field)
			}
			if strings.Contains(output, password) {
				t.Fatal("host refusal returned the refused value")
			}
			if after := importValue(t, d.Read, "SELECT COUNT(*) FROM connections"); after != before {
				t.Fatal("refused host declaration materialized a connection")
			}
		})
	}

	setEnv(t, "ATRIUM_HOST_CONFIG", writeFile(t, dir, "accepted.toml", hostConfigBody(defaultWebPushConnection)+strings.Replace(mail("imap_host", "::1"), "auth_method", "smtp_host = \"[2001:db8::1]:465\", auth_method", 1)))
	_, _, stop, err := startServeOnce(t, dir, false)
	if err != nil {
		t.Fatalf("serve with host and IPv6 literal fields did not start: %v", err)
	}
	stop()
	var imapHost, smtpHost string
	if err := d.Read.QueryRow("SELECT json_extract(config_json, '$.imap_host'), json_extract(config_json, '$.smtp_host') FROM connections WHERE id = 'mail/x'").Scan(&imapHost, &smtpHost); err != nil || imapHost != "::1" || smtpHost != "[2001:db8::1]:465" {
		t.Fatalf("stored hosts %q %q (%v), want the declared literals", imapHost, smtpHost, err)
	}
}

func TestConnectionHealthWatchSendsNothingForAStoredUserinfoEndpoint(t *testing.T) {
	requireCipher(t)
	setEnv(t, "ATRIUM_DB_KEY", rightKeyHex)
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	setEnv(t, "ATRIUM_HOST_CONFIG", writeFile(t, dir, "operator.toml", ""))
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = w.Write([]byte(`{"login":"atrium"}`))
	}))
	t.Cleanup(srv.Close)
	stored, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	const password = "stored-userinfo-must-not-be-sent"
	stored.User = url.UserPassword("operator", password)
	d := keyedDB(t, filepath.Join(dir, "atrium.db"))
	if _, err := d.Write.Exec("DELETE FROM connections WHERE id = 'service/grafana'"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.Exec("INSERT INTO connections (id, kind, label, config_source, config_json, transport_json, revision, enabled, state, checked_at, created_at, updated_at) VALUES ('service/grafana', 'service/grafana', 'Grafana', 'operator', ?, '{}', 1, 1, 'ok', 1, 0, 0)", fmt.Sprintf(`{"url":%q}`, stored.String())); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.Exec("INSERT INTO connection_capabilities (connection_id, name, enabled, config_json) VALUES ('service/grafana', 'viewer', 1, '{}')"); err != nil {
		t.Fatal(err)
	}
	startServeIn(t, dir)
	var reason string
	waitFor(t, func() bool {
		return d.Read.QueryRow("SELECT reason FROM connections WHERE id = 'service/grafana' AND state = 'failing'").Scan(&reason) == nil
	})
	if !strings.Contains(reason, "url") || !strings.Contains(reason, "userinfo") || strings.Contains(reason, password) {
		t.Fatalf("health reason %q, want the field and cause without the value", reason)
	}
	if n := requests.Load(); n != 0 {
		t.Fatalf("the health watch sent %d requests to a userinfo endpoint", n)
	}
}

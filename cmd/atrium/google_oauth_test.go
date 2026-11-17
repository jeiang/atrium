package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
)

type googleTokenFake struct {
	mu      sync.Mutex
	status  int
	refresh string
	forms   []url.Values
}

func (g *googleTokenFake) handler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/token" {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[{"id":"evt-1","summary":"Imported event","start":{"dateTime":"2026-11-02T10:00:00Z"},"end":{"dateTime":"2026-11-02T11:00:00Z"}}]}`))
		return
	}
	_ = r.ParseForm()
	g.mu.Lock()
	g.forms = append(g.forms, r.PostForm)
	status, refresh := g.status, g.refresh
	g.mu.Unlock()
	if status != http.StatusOK {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"Token has been expired or revoked."}`))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token": "access", "refresh_token": refresh, "token_type": "Bearer", "expires_in": 3600,
	})
}

func (g *googleTokenFake) setStatus(status int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.status = status
}

func (g *googleTokenFake) grants() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]string, len(g.forms))
	for i, f := range g.forms {
		out[i] = f.Get("grant_type")
	}
	return out
}

func keyedDB(t *testing.T, path string) *db.DB {
	t.Helper()
	key, err := db.ParseKey(rightKeyHex)
	if err != nil {
		t.Fatal(err)
	}
	d, err := db.Connect(path, db.WithKey(key))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func TestGoogleOAuthWizardStoresPerCapabilityRefreshTokensAndReportsHealth(t *testing.T) {
	fake := &googleTokenFake{status: http.StatusOK, refresh: "refresh-1"}
	srv := httptest.NewServer(http.HandlerFunc(fake.handler))
	t.Cleanup(srv.Close)
	requireCipher(t)
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	t.Setenv("ATRIUM_GOOGLE_AUTH_URL", srv.URL+"/auth")
	t.Setenv("ATRIUM_GOOGLE_TOKEN_URL", srv.URL+"/token")
	t.Setenv("ATRIUM_GOOGLE_API_URL", srv.URL+"/calendar/v3")
	t.Setenv("ATRIUM_CALENDAR_GOOGLE_API_URL", srv.URL+"/calendar/v3")
	base, logs, stop := startServeStoppable(t, dir)
	t.Cleanup(stop)
	browser := newWebClient(t, base, nil)
	browser.session("local")

	if page := nextGet(t, base, "/settings/connections/google/default", "Accept", "text/html"); page.status != http.StatusOK {
		t.Fatalf("google client page: %d %s", page.status, logs.String())
	}
	if saved := browser.do(http.MethodPut, "/web/v1/connections/google/oauth", map[string]any{"client_id": "client-id", "client_secret": "secret"}); saved.status != http.StatusOK {
		t.Fatalf("save client: %d %s", saved.status, saved.body)
	}
	start := func(capability string) *url.URL {
		t.Helper()
		got := browser.do(http.MethodPost, "/web/v1/connections/google/oauth/start", map[string]any{"capability": capability})
		data, _ := got.json["data"].(map[string]any)
		raw, _ := data["authorize_url"].(string)
		if got.status != http.StatusOK || raw == "" {
			t.Fatalf("start %s: %d %s", capability, got.status, got.body)
		}
		loc, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		return loc
	}
	complete := func(state, code string) apiReply {
		t.Helper()
		return browser.do(http.MethodPost, "/web/v1/connections/google/oauth/complete", map[string]any{"state": state, "code": code})
	}
	consent := start("calendar")
	query := consent.Query()
	if query.Get("code_challenge_method") != "S256" || query.Get("access_type") != "offline" || query.Get("prompt") != "consent" || query.Get("redirect_uri") != base+"/settings/connections/google/callback" {
		t.Fatalf("consent URL = %s", consent)
	}
	if !strings.Contains(query.Get("scope"), "calendar.events") {
		t.Errorf("calendar scope = %q", query.Get("scope"))
	}
	callback := nextGet(t, base, "/settings/connections/google/callback?state="+url.QueryEscape(query.Get("state"))+"&code=code-1")
	if want := "/settings/connections/google/default?state=" + url.QueryEscape(query.Get("state")) + "&code=code-1"; callback.status != http.StatusSeeOther || callback.header.Get("Location") != want {
		t.Fatalf("calendar callback: %d %q, want %q", callback.status, callback.header.Get("Location"), want)
	}
	if done := complete(query.Get("state"), "code-1"); done.status != http.StatusOK {
		t.Fatalf("calendar complete: %d %s", done.status, done.body)
	}
	d := keyedDB(t, filepath.Join(dir, "atrium.db"))
	token := queryValue(t, d.Read, "SELECT value FROM connection_credentials WHERE connection_id = 'google/default' AND slot = 'oauth/calendar/refresh'")
	if token != "refresh-1" {
		t.Fatalf("calendar refresh token = %q", token)
	}

	pasteState := start("gmail").Query().Get("state")
	if done := complete(pasteState, "code-2"); done.status != http.StatusOK {
		t.Fatalf("gmail paste-back: %d %s", done.status, done.body)
	}
	token = queryValue(t, d.Read, "SELECT value FROM connection_credentials WHERE connection_id = 'google/default' AND slot = 'oauth/gmail/refresh'")
	if token != "refresh-1" {
		t.Fatalf("gmail refresh token = %q", token)
	}
	if got := fake.grants(); len(got) != 2 || got[0] != "authorization_code" || got[1] != "authorization_code" {
		t.Fatalf("token grants = %v", got)
	}

	var count int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM operation_audit WHERE operation='connections.oauth-complete' AND outcome='ok' AND target='google/default' AND details_json=?", `{"connection_ids":["google/default"]}`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Errorf("successful consent operation audits = %d, want 2", count)
	}
	var leaked int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM operation_audit WHERE instr(details_json, ?) > 0 OR instr(details_json, ?) > 0 OR instr(details_json, ?) > 0", "refresh-1", query.Get("state"), pasteState).Scan(&leaked); err != nil {
		t.Fatal(err)
	}
	if leaked != 0 {
		t.Errorf("consent material appeared in %d operation audits", leaked)
	}

	if replayed := complete(pasteState, "code-2"); replayed.status < http.StatusBadRequest || replayed.status >= http.StatusInternalServerError {
		t.Errorf("replayed consent = %d, want a client error", replayed.status)
	}
	if got := fake.grants(); len(got) != 2 {
		t.Errorf("replayed consent exchanged a token: %d grants", len(got))
	}

	warmAt := warm(t, api)
	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("c1", "calendar_list", map[string]any{
		"start": "2026-11-01", "end": "2026-11-30", "calendar": "google",
	})))
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("listed")))
	if ran := runApp(t, "agent", "run", "--text", "what is on my calendar"); ran.code != cli.ExitOK {
		t.Fatalf("calendar run: %d %s", ran.code, ran.stderr)
	}
	requests := api.Requests()[warmAt:]
	if len(requests) != 2 {
		t.Fatalf("%d model requests, want 2", len(requests))
	}
	if content := fmt.Sprint(toolResultFor(t, requests[1], "c1")["content"]); !strings.Contains(content, "Imported event") {
		t.Errorf("calendar_list did not use the stored Google grant: %s", content)
	}

	if passed := browser.do(http.MethodPost, "/web/v1/connections/google/default/test", nil); passed.status != http.StatusOK {
		t.Fatalf("passing test: %d %s", passed.status, passed.body)
	}
	if got := googleHealth(t, d); got != "ok" {
		t.Fatalf("health after a passing test = %q", got)
	}
	fake.setStatus(http.StatusBadRequest)
	browser.do(http.MethodPost, "/web/v1/connections/google/default/test", nil)
	if got := googleHealth(t, d); got != "needs_reauth" {
		t.Fatalf("health after a revoked token = %q", got)
	}
}

func googleHealth(t *testing.T, d *db.DB) string {
	t.Helper()
	var state string
	if err := d.Read.QueryRow("SELECT state FROM connections WHERE id = 'google/default'").Scan(&state); err != nil {
		return ""
	}
	return state
}

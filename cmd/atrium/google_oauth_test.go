//go:build !modernc

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
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
	jar, _ := cookiejar.New(nil)
	s := &connectionSite{t: t, dir: dir, base: base, client: &http.Client{Jar: jar}, stop: stop}

	if code, body, _ := fetch(t, s.client, http.MethodGet, s.base+"/settings/connections/google", nil); code != http.StatusOK ||
		!strings.Contains(body, "Create the Google client") {
		t.Fatalf("google page: %d %s", code, logs.String())
	}
	noRedirect := &http.Client{Jar: s.client.Jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if code, _, _ := fetch(t, noRedirect, http.MethodPost, s.base+"/settings/connections/google",
		url.Values{"client_id": {"client-id"}, "client_secret": {"secret"}}); code != http.StatusSeeOther {
		t.Fatalf("save client: %d", code)
	}
	start := func(capability string) *url.URL {
		t.Helper()
		code, _, hdr := fetch(t, noRedirect, http.MethodPost, s.base+"/settings/connections/google/start", url.Values{"capability": {capability}})
		if code != http.StatusSeeOther {
			t.Fatalf("start %s: %d", capability, code)
		}
		loc, err := url.Parse(hdr.Get("Location"))
		if err != nil {
			t.Fatal(err)
		}
		return loc
	}
	consent := start("calendar")
	query := consent.Query()
	if query.Get("code_challenge_method") != "S256" || query.Get("access_type") != "offline" || query.Get("prompt") != "consent" {
		t.Fatalf("consent URL = %s", consent)
	}
	if !strings.Contains(query.Get("scope"), "calendar.events") {
		t.Errorf("calendar scope = %q", query.Get("scope"))
	}
	code, body, hdr := fetch(t, noRedirect, http.MethodGet, s.base+"/settings/connections/google/callback?state="+
		url.QueryEscape(query.Get("state"))+"&code=code-1", nil)
	if code != http.StatusSeeOther || !strings.HasSuffix(hdr.Get("Location"), "?connected=calendar") {
		t.Fatalf("calendar callback: %d %q %s", code, hdr.Get("Location"), body)
	}
	d := keyedDB(t, filepath.Join(dir, "atrium.db"))
	token := importValue(t, d.Read, "SELECT value FROM connection_credentials WHERE connection_id = 'google/default' AND slot = 'oauth/calendar/refresh'")
	if token != "refresh-1" {
		t.Fatalf("calendar refresh token = %q", token)
	}

	pasteState := start("gmail").Query().Get("state")
	pasted := s.base + "/settings/connections/google/callback?state=" + url.QueryEscape(pasteState) + "&code=code-2"
	code, _, hdr = fetch(t, noRedirect, http.MethodPost, s.base+"/settings/connections/google/paste", url.Values{"paste": {pasted}})
	if code != http.StatusSeeOther || !strings.HasSuffix(hdr.Get("Location"), "?connected=gmail") {
		t.Fatalf("gmail paste-back: %d %q", code, hdr.Get("Location"))
	}
	token = importValue(t, d.Read, "SELECT value FROM connection_credentials WHERE connection_id = 'google/default' AND slot = 'oauth/gmail/refresh'")
	if token != "refresh-1" {
		t.Fatalf("gmail refresh token = %q", token)
	}
	if got := fake.grants(); len(got) != 2 || got[0] != "authorization_code" || got[1] != "authorization_code" {
		t.Fatalf("token grants = %v", got)
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

	code, body, _ = fetch(t, s.client, http.MethodPost, s.base+"/settings/connections/google/test", url.Values{})
	if code != http.StatusOK || !strings.Contains(body, "The refresh token works.") {
		t.Fatalf("passing test: %d %s", code, body)
	}
	if got := googleHealth(t, d); got != "ok" {
		t.Fatalf("health after a passing test = %q", got)
	}
	fake.setStatus(http.StatusBadRequest)
	code, _, _ = fetch(t, s.client, http.MethodPost, s.base+"/settings/connections/google/test", url.Values{})
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("failing test: %d", code)
	}
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

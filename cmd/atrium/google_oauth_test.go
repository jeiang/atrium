//go:build !modernc

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/secrets"
)

type googleTokenFake struct {
	mu      sync.Mutex
	status  int
	refresh string
	forms   []url.Values
}

func (g *googleTokenFake) handler(w http.ResponseWriter, r *http.Request) {
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
	dir, _, f := moduleFixture(t, flagsExcept("", ""))
	t.Setenv("ATRIUM_GOOGLE_AUTH_URL", srv.URL+"/auth")
	t.Setenv("ATRIUM_GOOGLE_TOKEN_URL", srv.URL+"/token")
	setEnv(t, "ATRIUM_DB_KEY", rightKeyHex)
	t.Cleanup(func() {
		db.SetDefaultKey(db.Key{})
	})
	base, logs, stop := startServeStoppable(t, dir)
	t.Cleanup(stop)
	jar, _ := cookiejar.New(nil)
	s := &integrationSite{t: t, dir: dir, base: base, client: &http.Client{Jar: jar}, stop: stop}

	if code, body, _ := fetch(t, s.client, http.MethodGet, s.base+"/settings/integrations/google", nil); code != http.StatusOK ||
		!strings.Contains(body, "Create the Google client") {
		t.Fatalf("google page: %d %s", code, logs.String())
	}
	noRedirect := &http.Client{Jar: s.client.Jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if code, _, _ := fetch(t, noRedirect, http.MethodPost, s.base+"/settings/integrations/google",
		url.Values{"client_id": {"client-id"}, "client_secret": {"secret"}}); code != http.StatusSeeOther {
		t.Fatalf("save client: %d", code)
	}
	start := func(capability string) *url.URL {
		t.Helper()
		code, _, hdr := fetch(t, noRedirect, http.MethodPost, s.base+"/settings/integrations/google/start", url.Values{"capability": {capability}})
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
	if !strings.Contains(query.Get("scope"), "calendar.events") || !strings.Contains(query.Get("scope"), "calendar") {
		t.Errorf("calendar scope = %q", query.Get("scope"))
	}
	code, body, hdr := fetch(t, noRedirect, http.MethodGet, s.base+"/settings/integrations/google/callback?state="+
		url.QueryEscape(query.Get("state"))+"&code=code-1", nil)
	if code != http.StatusSeeOther || !strings.HasSuffix(hdr.Get("Location"), "?connected=calendar") {
		t.Fatalf("calendar callback: %d %q %s", code, hdr.Get("Location"), body)
	}
	d := keyedDB(t, filepath.Join(dir, "atrium.db"))
	token, err := secrets.NewIn(f.settings, d, d.Read, nil).Value(context.Background(), secrets.GoogleCalendarRefreshToken)
	if err != nil || token != "refresh-1" {
		t.Fatalf("calendar refresh token = %q, %v", token, err)
	}

	pasteState := start("gmail").Query().Get("state")
	pasted := s.base + "/settings/integrations/google/callback?state=" + url.QueryEscape(pasteState) + "&code=code-2"
	code, _, hdr = fetch(t, noRedirect, http.MethodPost, s.base+"/settings/integrations/google/paste", url.Values{"paste": {pasted}})
	if code != http.StatusSeeOther || !strings.HasSuffix(hdr.Get("Location"), "?connected=gmail") {
		t.Fatalf("gmail paste-back: %d %q", code, hdr.Get("Location"))
	}
	token, err = secrets.NewIn(f.settings, d, d.Read, nil).Value(context.Background(), secrets.GoogleGmailRefreshToken)
	if err != nil || token != "refresh-1" {
		t.Fatalf("gmail refresh token = %q, %v", token, err)
	}
	if got := fake.grants(); len(got) != 2 || got[0] != "authorization_code" || got[1] != "authorization_code" {
		t.Fatalf("token grants = %v", got)
	}

	if code, body := s.post("/google/test", nil); code != http.StatusOK || !strings.Contains(body, "The refresh token works.") {
		t.Fatalf("passing test: %d %s", code, body)
	}
	if got := googleHealth(t, d); got != "ok" {
		t.Fatalf("health after a passing test = %q", got)
	}
	fake.setStatus(http.StatusBadRequest)
	if code, _ := s.post("/google/test", nil); code != http.StatusUnprocessableEntity {
		t.Fatalf("failing test: %d", code)
	}
	if got := googleHealth(t, d); got != "needs_reauth" {
		t.Fatalf("health after a revoked token = %q", got)
	}
}

func googleHealth(t *testing.T, d *db.DB) string {
	t.Helper()
	var state string
	if err := d.Read.QueryRow("SELECT state FROM integration_health WHERE name = 'google'").Scan(&state); err != nil {
		return ""
	}
	return state
}

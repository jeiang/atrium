//go:build !modernc

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	grafanatools "github.com/jeiang/atrium/internal/assistant/tools/grafana"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/integrations"
	"github.com/jeiang/atrium/internal/notion"
	"github.com/jeiang/atrium/internal/secrets"
)

type fakeNotionAccess struct {
	mu    sync.Mutex
	pages int
}

func (f *fakeNotionAccess) handler(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if r.Header.Get("Authorization") != "Bearer ntn_good" {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"object":"error","status":401,"code":"unauthorized","message":"API token is invalid."}`))
		return
	}
	switch r.URL.Path {
	case "/v1/users/me":
		_, _ = w.Write([]byte(`{"object":"user","id":"bot","type":"bot"}`))
	case "/v1/search":
		results := make([]map[string]string, f.pages)
		for i := range results {
			results[i] = map[string]string{"object": "page", "id": "p"}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "results": results, "has_more": false, "next_cursor": nil})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func TestCredentialIntegrationsTestWithSpecificReasonsAndSaveOnlyWorkingSecrets(t *testing.T) {
	notionFake := &fakeNotionAccess{}
	notionSrv := httptest.NewServer(http.HandlerFunc(notionFake.handler))
	t.Cleanup(notionSrv.Close)
	t.Cleanup(notion.UseBaseURL(notionSrv.URL))
	grafana := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		switch {
		case token != "glsa_good" && token != "glsa_none":
			w.WriteHeader(http.StatusUnauthorized)
		case r.URL.Path == "/api/user":
			_, _ = w.Write([]byte(`{"login":"sa-atrium"}`))
		case r.URL.Path == "/api/search" && token == "glsa_none":
			w.WriteHeader(http.StatusForbidden)
		case r.URL.Path == "/api/search":
			_, _ = w.Write([]byte(`[]`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(grafana.Close)
	gone := httptest.NewServer(http.NotFoundHandler())
	gone.Close()

	dir, api := moduleFixture(t, flagsExcept("", ""))
	api.RejectKey = "sk-ant-bad"
	t.Setenv("ATRIUM_DB_KEY", rightKeyHex)
	t.Cleanup(func() { db.SetDefaultKey(db.Key{}) })
	unsetEnv(t, "ANTHROPIC_API_KEY", settingEnv(notion.TokenKey), settingEnv(grafanatools.KeyURL), settingEnv(grafanatools.KeyToken))
	t.Setenv("NOTION_TOKEN", "ntn_legacy")
	s := &integrationSite{t: t, dir: dir}
	s.start()

	stored := func(name string) bool {
		t.Helper()
		var n int
		if err := mustConnect(t, filepath.Join(dir, "atrium.db")).Read.QueryRow("SELECT COUNT(*) FROM secrets WHERE name = ?", name).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n == 1
	}
	try := func(name string, form url.Values, code int, state, reason string) {
		t.Helper()
		got, body := s.post("/"+name+"/test", form)
		reported := health(t, dir, name) == state
		if code != http.StatusOK {
			reported = strings.Contains(body, "test failed ("+state+")")
		}
		if got != code || !strings.Contains(body, reason) || !reported {
			t.Errorf("%s Test with %v: %d, want %d %s with %q", name, form, got, code, state, reason)
		}
		if secret := form.Get("secret"); secret != "" && strings.Contains(body, secret) {
			t.Errorf("%s page rendered the submitted credential", name)
		}
	}

	try("notion", url.Values{"secret": {"ntn_bad"}}, http.StatusUnprocessableEntity, integrations.NeedsReauth, "Notion refused the token")
	try("notion", url.Values{"secret": {"ntn_good"}}, http.StatusUnprocessableEntity, integrations.Failing, "no pages shared with the integration")
	if stored(notion.TokenKey) {
		t.Error("a Notion token that failed Test was stored")
	}
	notionFake.mu.Lock()
	notionFake.pages = 3
	notionFake.mu.Unlock()
	try("notion", url.Values{"secret": {"ntn_good"}}, http.StatusOK, integrations.OK, "3 pages shared")
	if !stored(notion.TokenKey) {
		t.Error("a working Notion token was not stored")
	}

	try("grafana", url.Values{"url": {grafana.URL}, "secret": {"glsa_bad"}}, http.StatusUnprocessableEntity, integrations.NeedsReauth, "Grafana refused the token")
	try("grafana", url.Values{"url": {grafana.URL}, "secret": {"glsa_none"}}, http.StatusUnprocessableEntity, integrations.Failing, "sa-atrium may not search dashboards")
	try("grafana", url.Values{"url": {gone.URL}, "secret": {"glsa_good"}}, http.StatusUnprocessableEntity, integrations.Failing, "connection refused")
	if stored(grafanatools.KeyToken) {
		t.Error("a Grafana token that failed Test was stored")
	}
	try("grafana", url.Values{"url": {grafana.URL}, "secret": {"glsa_good"}}, http.StatusOK, integrations.OK, "signed in as sa-atrium")

	page := s.page()
	if !strings.Contains(page, "monthly spend limit") {
		t.Error("the Anthropic form lacks the spend-limited workspace guidance")
	}
	try("anthropic", url.Values{"secret": {"sk-ant-bad"}}, http.StatusUnprocessableEntity, integrations.NeedsReauth, "Anthropic refused the key")
	if stored(secrets.AnthropicAPIKey) {
		t.Error("a refused Anthropic key was stored")
	}
	try("anthropic", url.Values{"secret": {"sk-ant-good"}}, http.StatusOK, integrations.OK, "models available")
	if !stored(secrets.AnthropicAPIKey) || strings.Contains(s.page(), "sk-ant-good") {
		t.Error("the working Anthropic key was not stored write-only")
	}

	if got := runApp("settings", "get", notion.TokenKey); strings.Contains(got.stdout, "ntn_legacy") || !strings.Contains(got.stderr, "ATRIUM_NOTION_NOTION_TOKEN") {
		t.Errorf("NOTION_TOKEN is still read or not pointed at its replacement: %q %q", got.stdout, got.stderr)
	}
}

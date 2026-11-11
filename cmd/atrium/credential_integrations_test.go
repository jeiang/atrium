//go:build !modernc

package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/cli"

	grafanatools "github.com/jeiang/atrium/internal/assistant/tools/grafana"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/integrations"
	"github.com/jeiang/atrium/internal/secrets"
)

func TestCredentialIntegrationsTestWithSpecificReasonsAndSaveOnlyWorkingSecrets(t *testing.T) {
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

	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	api.RejectKey = "sk-ant-bad"
	setEnv(t, "ATRIUM_DB_KEY", rightKeyHex)
	t.Cleanup(func() { db.SetDefaultKey(db.Key{}) })
	unsetEnv(t, "ANTHROPIC_API_KEY", settingEnv(grafanatools.KeyURL), settingEnv(grafanatools.KeyToken))
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

	if got := runApp(t, "settings", "get", secrets.AnthropicAPIKey); got.code != cli.ExitError || strings.Contains(got.stdout, "sk-ant-good") || !strings.Contains(got.stderr, "is a secret") {
		t.Errorf("settings get of a secret: exit %d stdout %q stderr %q", got.code, got.stdout, got.stderr)
	}
}

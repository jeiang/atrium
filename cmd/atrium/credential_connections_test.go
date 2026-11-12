//go:build !modernc

package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	grafanatools "github.com/jeiang/atrium/internal/assistant/tools/grafana"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
)

func servicesHostConfig(t *testing.T, dir, grafanaURL, anthropicURL string) string {
	t.Helper()
	body := fmt.Sprintf(`[connections."service/grafana"]
kind = "service/grafana"
label = "Grafana"
config = { url = %q }
capabilities = ["viewer"]

[connections."service/anthropic"]
kind = "service/anthropic"
label = "Anthropic"
config = { url = %q }
capabilities = ["models"]
`, grafanaURL, anthropicURL)
	path := filepath.Join(dir, "services.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestConnectionTestsSaveOnlyWorkingCredentialsAndNeverRenderThem(t *testing.T) {
	api := anthropictest.New(t)
	api.RejectKey = "sk-ant-bad"
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

	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	setEnv(t, "ATRIUM_DB_KEY", rightKeyHex)
	t.Cleanup(func() { db.SetDefaultKey(db.Key{}) })
	setEnv(t, "ATRIUM_HOST_CONFIG", servicesHostConfig(t, dir, grafana.URL, api.URL))
	setEnv(t, settingEnv(grafanatools.KeyURL), grafana.URL)
	s := &connectionSite{t: t, dir: dir}
	s.start()

	stored := func(id, slot string) bool {
		t.Helper()
		var n int
		if err := mustConnect(t, filepath.Join(dir, "atrium.db")).Read.QueryRow(
			"SELECT COUNT(*) FROM connection_credentials WHERE connection_id = ? AND slot = ?", id, slot).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n == 1
	}
	try := func(id string, form url.Values, code int, state, reason, secret string) {
		t.Helper()
		got, body := s.conn("/test", form)
		if got != code || !strings.Contains(body, reason) {
			t.Errorf("%s Test with %v: %d %s, want %d %s", id, form, got, body, code, reason)
		}
		if state != "" && connectionState(t, dir, id) != state {
			t.Errorf("%s state = %q, want %q", id, connectionState(t, dir, id), state)
		}
		if secret != "" && strings.Contains(body, secret) {
			t.Errorf("%s page rendered the submitted credential", id)
		}
	}

	try("service/grafana", url.Values{"id": {"service/grafana"}, "api_key": {"glsa_bad"}},
		http.StatusUnprocessableEntity, "", "Grafana refused the token", "glsa_bad")
	if stored("service/grafana", "api_key") {
		t.Error("a Grafana token that failed Test was stored")
	}
	try("service/grafana", url.Values{"id": {"service/grafana"}, "api_key": {"glsa_none"}},
		http.StatusUnprocessableEntity, "", "may not search dashboards", "glsa_none")
	if stored("service/grafana", "api_key") {
		t.Error("a Grafana token that failed Test was stored")
	}
	try("service/grafana", url.Values{"id": {"service/grafana"}, "api_key": {"glsa_good"}},
		http.StatusOK, "ok", "signed in as sa-atrium", "")

	try("service/anthropic", url.Values{"id": {"service/anthropic"}, "api_key": {"sk-ant-bad"}},
		http.StatusUnprocessableEntity, "", "Anthropic refused the key", "sk-ant-bad")
	if stored("service/anthropic", "api_key") {
		t.Error("a refused Anthropic key was stored")
	}
	try("service/anthropic", url.Values{"id": {"service/anthropic"}, "api_key": {"sk-ant-good"}},
		http.StatusOK, "ok", "models available", "")
	if !stored("service/anthropic", "api_key") {
		t.Error("the working Anthropic key was not stored")
	}
	if page := s.page(); strings.Contains(page, "sk-ant-good") || strings.Contains(page, "glsa_good") {
		t.Error("the settings page rendered a stored credential")
	}

	if got := runApp(t, "settings", "get", "anthropic_api_key"); got.code != cli.ExitError || !strings.Contains(got.stderr, "service/anthropic credential api_key") {
		t.Errorf("settings get of the retired Anthropic secret: exit %d stdout %q stderr %q", got.code, got.stdout, got.stderr)
	}
}

func TestGrafanaConnectionNeedsAViewerToken(t *testing.T) {
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	setEnv(t, "ATRIUM_HOST_CONFIG", servicesHostConfig(t, dir, "http://grafana.invalid", "https://api.anthropic.com"))
	ran := runApp(t, "connections", "test", "service/grafana")
	if ran.code != cli.ExitError || !strings.Contains(ran.stdout+ran.stderr, "no viewer token") {
		t.Fatalf("connections test without a token: exit %d stdout %q stderr %q", ran.code, ran.stdout, ran.stderr)
	}
	var state string
	if err := mustConnect(t, filepath.Join(dir, "atrium.db")).Read.QueryRow(
		"SELECT state FROM connections WHERE id = 'service/grafana'").Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state == "ok" {
		t.Fatalf("a keyless Grafana connection reported ok")
	}
	_ = fmt.Sprint(state)
}

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/settings"
)

type pinRow struct {
	Value     string `json:"value"`
	Source    string `json:"source"`
	PinSource string `json:"pin_source"`
}

func pinRowOf(t *testing.T, r settingsRun) pinRow {
	t.Helper()
	if r.code != cli.ExitOK {
		t.Fatalf("exit %d, stderr %q", r.code, r.stderr)
	}
	var row pinRow
	if err := json.Unmarshal([]byte(r.stdout), &row); err != nil {
		t.Fatalf("stdout %q: %v", r.stdout, err)
	}
	return row
}

func writeFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func hostSettings(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	db := filepath.Join(dir, "atrium.db")
	setEnv(t, "ATRIUM_DB", db)
	setEnv(t, "ATRIUM_CACHE_DIR", filepath.Join(dir, "cache"))
	setEnv(t, "ATRIUM_PUBLIC_HOST", "")
	setEnv(t, "ATRIUM_CONFIG", "")
	setEnv(t, "ATRIUM_HOST_CONFIG", "")
	setEnv(t, "ATRIUM_MEDIA_MEDIA_QBITTORRENT_URL", "")
	setEnv(t, "ATRIUM_MEDIA_MEDIA_ROOT", "")
	return dir, db
}

func TestHostConfigurationIsReadOnlyAndWinsOverTheDatabase(t *testing.T) {
	t.Parallel()
	dir, db := hostSettings(t)
	const key = "public_url"

	host := writeFile(t, dir, "host.toml", "[core]\npublic_url = \"https://atrium.host.example\"\n[media]\nmedia_root = \"/srv/media\"\n")
	setEnv(t, "ATRIUM_HOST_CONFIG", host)

	got := pinRowOf(t, runSettings(t, "get", key))
	if got.Value != "https://atrium.host.example" || got.Source != "host" {
		t.Errorf("host value: %+v", got)
	}
	for _, verb := range []struct {
		args []string
		want string
	}{
		{[]string{"set", key, "https://other.example"}, "core.public_url"},
		{[]string{"set", "media_root", "/other"}, "media.media_root"},
		{[]string{"unset", key}, "core.public_url"},
	} {
		r := runSettings(t, verb.args...)
		if r.code != cli.ExitError || !strings.Contains(r.stderr, "host configuration and is not editable here") || !strings.Contains(r.stderr, verb.want) {
			t.Errorf("%s: exit %d stderr %q, want %s", verb.args[0], r.code, r.stderr, verb.want)
		}
	}
	if r := runSettings(t, "set", "calendar_dir", "/srv/calendar"); r.code != cli.ExitError || !strings.Contains(r.stderr, "calendar.calendar_dir") {
		t.Errorf("set a host path: exit %d stderr %q", r.code, r.stderr)
	}
	if _, ok := storedSetting(t, db, key); ok {
		t.Error("a refused host write stored the value")
	}

	secret := writeFile(t, dir, "oidc-secret", "client-secret-value\n")
	setEnv(t, "ATRIUM_HOST_CONFIG", writeFile(t, dir, "secret.toml", "[connections.\"service/oidc\"]\nkind = \"service/oidc\"\nconfig = { issuer = \"https://idp.example.org\" }\ncapabilities = [\"discovery\"]\ncredential_files = { client_secret = \""+secret+"\" }\n"))
	if r := runApp(t, "connections", "list", "--json"); r.code != cli.ExitOK || !strings.Contains(r.stdout, `"slot": "client_secret"`) || !strings.Contains(r.stdout, `"source": "host_file"`) {
		t.Errorf("host connection credential: exit %d stdout %q stderr %q", r.code, r.stdout, r.stderr)
	}

	setEnv(t, "ATRIUM_HOST_CONFIG", writeFile(t, dir, "env.toml", "[core]\npublic_url = \"https://atrium.host.example\"\n"))
	setEnv(t, "ATRIUM_CORE_PUBLIC_URL", "https://atrium.env.example")
	got = pinRowOf(t, runSettings(t, "get", key))
	if got.Value != "https://atrium.env.example" || got.Source != "host" {
		t.Errorf("env override: %+v", got)
	}
	setEnv(t, "ATRIUM_CORE_PUBLIC_URL", "")

	urlFile := writeFile(t, dir, "public-url", "https://atrium.file.example\n")
	setEnv(t, "ATRIUM_HOST_CONFIG", writeFile(t, dir, "file.toml", "[core]\npublic_url_file = \""+urlFile+"\"\n"))
	if got := pinRowOf(t, runSettings(t, "get", key)); got.Value != "https://atrium.file.example" || got.Source != "host" {
		t.Errorf("host file value: %+v", got)
	}

	for name, c := range map[string]struct{ body, want string }{
		"secret direct": {"[auth]\noidc_client_secret = \"x\"\n", `connections."service/oidc".credential_files.client_secret`},
		"operator key":  {"[assistant]\nmonthly_budget_usd = 1\n", "atrium settings set monthly_budget_usd"},
		"tuning key":    {"[assistant]\nmax_steps_chat = 10\n", "compiled constant internal/assistant/loop/tuning.go:TuningMaxStepsChat"},
		"database key":  {"[db]\nkey = \"00\"\n", "use ATRIUM_DB_KEY_FILE"},
		"unknown key":   {"[core]\nnot_a_key = 1\n", "unknown host key \"core.not_a_key\""},
	} {
		setEnv(t, "ATRIUM_HOST_CONFIG", writeFile(t, dir, "bad.toml", c.body))
		r := runSettings(t, "get", key)
		if r.code != cli.ExitError || !strings.Contains(r.stderr, c.want) {
			t.Errorf("%s: exit %d stderr %q, want %q", name, r.code, r.stderr, c.want)
		}
	}

	setEnv(t, "ATRIUM_HOST_CONFIG", writeFile(t, dir, "connection.toml", "[connections.\"service/qbittorrent\"]\nkind = \"service/qbittorrent\"\nconfig = { url = \"http://127.0.0.1:8081\" }\ncapabilities = [\"api\"]\n"))
	if r := runApp(t, "connections", "list", "--json"); r.code != cli.ExitOK || !strings.Contains(r.stdout, "service/qbittorrent") {
		t.Errorf("host connection: exit %d stdout %q stderr %q", r.code, r.stdout, r.stderr)
	}
}

func TestRemovedEnvironmentAndFilePinsAreRefused(t *testing.T) {
	t.Parallel()
	dir, _ := hostSettings(t)

	for name, c := range map[string]struct {
		env  map[string]string
		want string
	}{
		"credential env": {env: map[string]string{"ATRIUM_MEDIA_MEDIA_QBITTORRENT_API_KEY": "qbt"}, want: `connections."service/qbittorrent".credential_files.api_key`},
		"credential file env": {env: map[string]string{"ATRIUM_AUTH_OIDC_CLIENT_SECRET_FILE": writeFile(t, dir, "oidc-client-secret", "client-secret-value\n")},
			want: `connections."service/oidc".credential_files.client_secret`},
		"tuning env":    {env: map[string]string{"ATRIUM_MEDIA_MEDIA_QBITTORRENT_TIMEOUT_SECONDS": "30"}, want: "compiled constant internal/media/tuning.go:TuningMediaQbittorrentTimeoutSeconds"},
		"operator env":  {env: map[string]string{"ATRIUM_ASSISTANT_MONTHLY_BUDGET_USD": "120"}, want: "atrium settings set monthly_budget_usd"},
		"host env":      {env: map[string]string{"ATRIUM_MEDIA_MEDIA_QBITTORRENT_URL": "http://q.invalid"}, want: `connections."service/qbittorrent".config.url`},
		"anthropic env": {env: map[string]string{"ANTHROPIC_API_KEY": "sk-test"}, want: `connections."service/anthropic".credential_files.api_key`},
	} {
		for key, value := range c.env {
			setEnv(t, key, value)
		}
		r := runSettings(t, "get", "tz")
		if r.code != cli.ExitError || !strings.Contains(r.stderr, "no longer read") || !strings.Contains(r.stderr, c.want) {
			t.Errorf("%s: exit %d stderr %q, want %q", name, r.code, r.stderr, c.want)
		}
		for key := range c.env {
			setEnv(t, key, "")
		}
	}

	setEnv(t, "ATRIUM_CONFIG", writeFile(t, dir, "staged.toml", "[assistant]\nmonthly_budget_usd = 120\n"))
	if r := runSettings(t, "get", "tz"); r.code != cli.ExitError || !strings.Contains(r.stderr, "ATRIUM_CONFIG is no longer read") {
		t.Errorf("legacy pin file: exit %d stderr %q", r.code, r.stderr)
	}
	setEnv(t, "ATRIUM_CONFIG", "")
	def, _ := settings.LookupOf("monthly_budget_usd")
	if got := pinRowOf(t, runSettings(t, "get", "monthly_budget_usd")); got.Value != def.Default || got.Source != "default" {
		t.Errorf("operator default after removing the pin file: %+v, want %s", got, def.Default)
	}
}

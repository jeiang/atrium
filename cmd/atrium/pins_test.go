package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/cli"
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
	if got.Value != "https://atrium.host.example" || got.Source != "pin" || !strings.Contains(got.PinSource, host) || !strings.Contains(got.PinSource, "core."+key) {
		t.Errorf("host value: %+v", got)
	}
	for _, verb := range [][]string{{"set", key, "https://other.example"}, {"set", "media_root", "/other"}, {"unset", key}} {
		r := runSettings(t, verb...)
		if r.code != cli.ExitError || !strings.Contains(r.stderr, "pinned by host") {
			t.Errorf("%s: exit %d stderr %q", verb[0], r.code, r.stderr)
		}
	}
	if r := runSettings(t, "set", "media_qbittorrent_url", "http://127.0.0.1:8081"); r.code != cli.ExitError || !strings.Contains(r.stderr, "host configuration and is not editable here") {
		t.Errorf("set a cut-over host key: exit %d stderr %q", r.code, r.stderr)
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
	if got.Value != "https://atrium.env.example" || got.PinSource != "env ATRIUM_CORE_PUBLIC_URL" {
		t.Errorf("env override: %+v", got)
	}
	setEnv(t, "ATRIUM_CORE_PUBLIC_URL", "")

	urlFile := writeFile(t, dir, "public-url", "https://atrium.file.example\n")
	setEnv(t, "ATRIUM_HOST_CONFIG", writeFile(t, dir, "file.toml", "[core]\npublic_url_file = \""+urlFile+"\"\n"))
	if got := pinRowOf(t, runSettings(t, "get", key)); got.Value != "https://atrium.file.example" || got.Source != "pin" {
		t.Errorf("host file value: %+v", got)
	}

	for name, c := range map[string]struct{ body, want string }{
		"secret direct": {"[auth]\noidc_client_secret = \"x\"\n", "is a host bootstrap input; use the dedicated option"},
		"operator key":  {"[assistant]\nmonthly_budget_usd = 1\n", "not host configuration; use the operator setting assistant.monthly_budget_usd"},
		"tuning key":    {"[assistant]\nmax_steps_chat = 10\n", "compiled constant internal/assistant/loop/tuning.go:TuningMaxStepsChat"},
		"cutover host":  {"[media]\nmedia_qbittorrent_url = \"http://127.0.0.1:8081\"\n", "host configuration service/qbittorrent.config.url"},
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

func TestCutoverOperatorAndTuningPinsAreRefused(t *testing.T) {
	t.Parallel()
	dir, _ := hostSettings(t)

	for name, c := range map[string]struct {
		env  map[string]string
		file string
		want string
	}{
		"credential env":  {env: map[string]string{"ATRIUM_MEDIA_MEDIA_QBITTORRENT_API_KEY": "qbt"}, want: "connection field service/qbittorrent credential api_key"},
		"credential file": {file: "[media]\nmedia_qbittorrent_api_key = \"qbt\"\n", want: "connection field service/qbittorrent credential api_key"},
		"tuning config":   {file: "[media]\nmedia_qbittorrent_timeout_seconds = 30\n", want: "compiled constant internal/media/tuning.go:TuningMediaQbittorrentTimeoutSeconds"},
		"tuning env":      {env: map[string]string{"ATRIUM_MEDIA_MEDIA_QBITTORRENT_TIMEOUT_SECONDS": "30"}, want: "compiled constant internal/media/tuning.go:TuningMediaQbittorrentTimeoutSeconds"},
	} {
		for key, value := range c.env {
			setEnv(t, key, value)
		}
		if c.file != "" {
			setEnv(t, "ATRIUM_CONFIG", writeFile(t, dir, "cutover.toml", c.file))
		}
		r := runSettings(t, "get", "tz")
		if r.code != cli.ExitError || !strings.Contains(r.stderr, "cut over") || !strings.Contains(r.stderr, c.want) {
			t.Errorf("%s: exit %d stderr %q, want %q", name, r.code, r.stderr, c.want)
		}
		for key := range c.env {
			setEnv(t, key, "")
		}
		setEnv(t, "ATRIUM_CONFIG", "")
	}

	setEnv(t, "ATRIUM_CONFIG", writeFile(t, dir, "staged.toml", "[assistant]\nmonthly_budget_usd = 120\n"))
	if got := pinRowOf(t, runSettings(t, "get", "monthly_budget_usd")); got.Value != "120" || got.Source != "pin" {
		t.Errorf("a non-cutover operator pin: %+v", got)
	}
}

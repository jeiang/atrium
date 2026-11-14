package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
	grafanatools "github.com/jeiang/atrium/internal/grafana"
	kbexport "github.com/jeiang/atrium/internal/kb/export"
	"github.com/jeiang/atrium/internal/media"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/serverops"
)

func seedSetting(t *testing.T, d *db.DB, key, value string) {
	t.Helper()
	if _, err := d.Write.Exec("INSERT OR REPLACE INTO settings (key, value) VALUES (?, ?)", key, value); err != nil {
		t.Fatal(err)
	}
}

func TestHostRowsCannotChangeBehavior(t *testing.T) {
	dir, f, d := obsidianFixture(t)
	vault := t.TempDir()
	setEnv(t, settingEnv("public_url"), "")
	setEnv(t, settingEnv(kbexport.ObsidianDirKey), "")
	setEnv(t, "ATRIUM_MEDIA_MEDIA_DOWNLOAD_PATH_PREFIX", filepath.Join(dir, "downloads"))

	seedSetting(t, d, "public_url", "https://poisoned.example")
	seedSetting(t, d, kbexport.ObsidianDirKey, vault)
	seedSetting(t, d, media.KeyDownloadPrefix, vault)
	seedSetting(t, d, media.KeyQbitURL, "http://poisoned.invalid:8081")

	if got := pinRowOf(t, runSettings(t, "get", "public_url")); got.Value != "" || got.Source != "host" {
		t.Errorf("a host row reached the public URL: %+v", got)
	}
	if got := pinRowOf(t, runSettings(t, "get", kbexport.ObsidianDirKey)); got.Value != "" || got.Source != "host" {
		t.Errorf("a host row reached the export folder: %+v", got)
	}
	if got := pinRowOf(t, runSettings(t, "get", media.KeyDownloadPrefix)); got.Value != filepath.Join(dir, "downloads") || got.Source != "host" {
		t.Errorf("a host row overrode the host download prefix: %+v", got)
	}
	runExport(t, f, d)
	entries, err := os.ReadDir(vault)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("a host row granted an export into %s: %d entries", vault, len(entries))
	}
}

func TestConnectionOwnedURLRowCannotRedirectTheSampler(t *testing.T) {
	fake := &fakeOpsGrafana{}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	dir, api, f := moduleFixture(t, flagsExcept("", ""))
	setEnv(t, "ATRIUM_HOST_CONFIG", grafanaHost(t, dir, srv.URL))
	setEnv(t, settingEnv(serverops.KeyHosts), "node-a")
	warm(t, api)
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), db.Key{})
	defer func() { _ = d.Close() }()
	seedSetting(t, d, grafanatools.KeyURL, "http://127.0.0.1:1")
	runOpsSampler(t, f, d, samplesExist(t, d))
	if len(fake.seen()) == 0 {
		t.Errorf("the sampler used the connection-owned URL row instead of the host connection")
	}
}

func TestHostDeclaredModuleFlagWinsAndIsReadOnly(t *testing.T) {
	dir, _, _ := moduleFixture(t, flagsExcept(module.Mail, "true"))
	raw, err := os.ReadFile(defaultHostConfig)
	if err != nil {
		t.Fatal(err)
	}
	host := filepath.Join(dir, "host-flag.toml")
	if err := os.WriteFile(host, append(raw, []byte("\n[core]\nmail_enabled = true\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	setEnv(t, "ATRIUM_HOST_CONFIG", host)
	if ran := runApp(t, "settings", "list"); ran.code != cli.ExitOK {
		t.Fatalf("settings list: exit %d stderr %q", ran.code, ran.stderr)
	}
	path := filepath.Join(dir, "atrium.db")
	seedSetting(t, mustConnect(t, path, db.Key{}), module.FlagKey(module.Mail), "false")

	if got := pinRowOf(t, runSettings(t, "get", module.FlagKey(module.Mail))); got.Value != "true" || !strings.Contains(got.PinSource, "host") {
		t.Errorf("a database row overrode the host module flag: %+v", got)
	}
	got := runSettings(t, "set", module.FlagKey(module.Mail), "false")
	if got.code != cli.ExitError || !strings.Contains(got.stderr, "pinned by host") {
		t.Errorf("setting a host-declared flag: exit %d stderr %q", got.code, got.stderr)
	}
	if value, ok := storedSetting(t, path, module.FlagKey(module.Mail)); !ok || value != "false" {
		t.Errorf("a refused flag write changed the row: %q %v", value, ok)
	}
	if status, _, _ := fetch(t, http.DefaultClient, http.MethodGet, startServeIn(t, dir)+"/triage", nil); status != http.StatusOK {
		t.Errorf("GET /triage = %d, want the host-declared flag to enable mail", status)
	}
}

func TestUnpinnedHostWritesAreRefused(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "atrium.db")
	setEnv(t, "ATRIUM_DB", path)
	setEnv(t, "ATRIUM_CACHE_DIR", filepath.Join(dir, "cache"))

	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"set", "public_url", "https://other.example"}, "core.public_url"},
		{[]string{"set", "media_download_path_prefix", "/srv/downloads"}, "media.media_download_path_prefix"},
		{[]string{"unset", "export_obsidian_dir"}, "export.export_obsidian_dir"},
	} {
		r := runSettings(t, c.args...)
		if r.code != cli.ExitError || !strings.Contains(r.stderr, "host configuration and is not editable here") || !strings.Contains(r.stderr, c.want) {
			t.Errorf("%s %s: exit %d stderr %q, want %s", c.args[0], c.args[1], r.code, r.stderr, c.want)
		}
	}

	seedAllFlags(t)
	w := asstBrowse(t, startServeIn(t, dir))
	status, page, _ := w.get("/settings/export", false)
	if status != http.StatusOK {
		t.Fatalf("GET integrations = %d", status)
	}
	if strings.Contains(page, `name="export_obsidian_dir"`) {
		t.Errorf("the host folder is still an editable control")
	}
	form := url.Values{"export_obsidian_dir": {"/srv/vault"}}
	if status, body, _ := fetch(t, w.client, http.MethodPost, w.base+"/settings/sections/export_obsidian", form); status != http.StatusOK {
		t.Fatalf("folder save = %d: %.300s", status, body)
	}

	for _, key := range []string{"public_url", "media_download_path_prefix", "export_obsidian_dir"} {
		if value, ok := storedSetting(t, path, key); ok {
			t.Errorf("a refused host write stored %s = %q", key, value)
		}
	}
}

func TestHostFileRefusesARemovedLocalChatSecret(t *testing.T) {
	dir := t.TempDir()
	hostPath := filepath.Join(dir, "host.toml")
	if err := os.WriteFile(hostPath, []byte("[mail]\nmail_chat_key = \"raw-secret-sentinel\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	setEnv(t, "ATRIUM_HOST_CONFIG", hostPath)
	setEnv(t, "ATRIUM_DB", filepath.Join(dir, "atrium.db"))
	setEnv(t, "ATRIUM_CACHE_DIR", filepath.Join(dir, "cache"))
	got := runApp(t, "settings", "list")
	if got.code == cli.ExitOK {
		t.Fatalf("a raw secret in the host file was accepted: %s", got.stdout)
	}
	if !strings.Contains(got.stderr, "mail.mail_chat_key was removed") || !strings.Contains(got.stderr, "service/anthropic") {
		t.Errorf("the refusal does not name the removed key and its replacement: %q", got.stderr)
	}
	if strings.Contains(got.stdout+got.stderr, "raw-secret-sentinel") {
		t.Errorf("the refusal printed the secret: %q", got.stderr)
	}
}

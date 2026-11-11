package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	grafanatools "github.com/jeiang/atrium/internal/assistant/tools/grafana"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
	kbexport "github.com/jeiang/atrium/internal/kb/export"
	"github.com/jeiang/atrium/internal/media"
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
	setEnv(t, settingEnv(media.KeyRoot), filepath.Join(dir, "media"))

	seedSetting(t, d, "public_url", "https://poisoned.example")
	seedSetting(t, d, kbexport.ObsidianDirKey, vault)
	seedSetting(t, d, media.KeyRoot, vault)
	seedSetting(t, d, media.KeyQbitURL, "http://poisoned.invalid:8081")

	if got := pinRowOf(t, runSettings(t, "get", "public_url")); got.Value != "" || got.Source != "host" {
		t.Errorf("a host row reached the public URL: %+v", got)
	}
	if got := pinRowOf(t, runSettings(t, "get", kbexport.ObsidianDirKey)); got.Value != "" || got.Source != "host" {
		t.Errorf("a host row reached the export folder: %+v", got)
	}
	if got := pinRowOf(t, runSettings(t, "get", media.KeyRoot)); got.Value != filepath.Join(dir, "media") || got.Source != "host" {
		t.Errorf("a host row overrode the host media root: %+v", got)
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
	setEnv(t, settingEnv(grafanatools.KeyURL), srv.URL)
	setEnv(t, hostEnv("grafana", grafanatools.KeyMetricsUID), "metrics")
	setEnv(t, settingEnv(serverops.KeyHosts), "node-a")
	warm(t, api)
	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
	defer func() { _ = d.Close() }()
	seedSetting(t, d, grafanatools.KeyURL, "http://127.0.0.1:1")
	runOpsSampler(t, f, d, samplesExist(t, d))
	if len(fake.seen()) == 0 {
		t.Errorf("the sampler used the connection-owned URL row instead of the host connection")
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
		{[]string{"set", "media_root", "/srv/media"}, "media.media_root"},
		{[]string{"unset", "export_obsidian_dir"}, "export.export_obsidian_dir"},
	} {
		r := runSettings(t, c.args...)
		if r.code != cli.ExitError || !strings.Contains(r.stderr, "host configuration and is not editable here") || !strings.Contains(r.stderr, c.want) {
			t.Errorf("%s %s: exit %d stderr %q, want %s", c.args[0], c.args[1], r.code, r.stderr, c.want)
		}
	}

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

	for _, key := range []string{"public_url", "media_root", "export_obsidian_dir"} {
		if value, ok := storedSetting(t, path, key); ok {
			t.Errorf("a refused host write stored %s = %q", key, value)
		}
	}
}

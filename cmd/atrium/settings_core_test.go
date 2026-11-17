package main

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/cli"
)

func TestCoreSettingsRejectBadValuesAndDriveTheWebLayer(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	setEnv(t, "ATRIUM_DB", filepath.Join(dir, "atrium.db"))
	setEnv(t, "ATRIUM_CACHE_DIR", filepath.Join(dir, "cache"))
	setEnv(t, "ATRIUM_PUBLIC_HOST", "")

	for _, tc := range []struct {
		key, value, want string
		code             int
	}{
		{"tz", "Mars/Olympus", "must be an IANA time zone", cli.ExitUsage},
		{"media_qbittorrent_url", "http://127.0.0.1:8081", `use connections."service/qbittorrent".config.url`, cli.ExitError},
	} {
		got := runSettings(t, "set", tc.key, tc.value)
		if got.code != tc.code || got.stdout != "" || !strings.Contains(got.stderr, tc.want) {
			t.Errorf("set %s %s: exit %d stdout %q stderr %q, want %q", tc.key, tc.value, got.code, got.stdout, got.stderr, tc.want)
		}
	}
	for _, key := range []string{"body_cap_bytes", "web_page_size", "http_timeout_seconds"} {
		if got := runSettings(t, "set", key, "1"); got.code == cli.ExitOK || !strings.Contains(got.stderr, "compiled constant") {
			t.Errorf("set %s: exit %d stderr %q, want a compiled-constant refusal", key, got.code, got.stderr)
		}
	}
	if got := runSettings(t, "set", "tz", "America/Barbados"); got.code != cli.ExitOK {
		t.Fatalf("set tz: exit %d %q", got.code, got.stderr)
	}

	c := webG4(t, startServeIn(t, dir))
	post := func(text string) int {
		return c.do(http.MethodPost, "/web/v1/threads", map[string]any{"text": text}).status
	}
	if status := post(strings.Repeat("a", 4096)); status == http.StatusRequestEntityTooLarge {
		t.Errorf("POST below the 1 MiB body cap = %d, want it past the size check", status)
	}
	if status := post(strings.Repeat("a", 1<<20)); status != http.StatusRequestEntityTooLarge {
		t.Errorf("POST above the 1 MiB body cap = %d, want 413", status)
	}
}

func TestInitialTimezoneSeedsAFreshDatabaseAndStaysEditable(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	setEnv(t, "ATRIUM_DB", filepath.Join(dir, "atrium.db"))
	setEnv(t, "ATRIUM_CACHE_DIR", filepath.Join(dir, "cache"))
	setEnv(t, "ATRIUM_PUBLIC_HOST", "")
	setEnv(t, "ATRIUM_INITIAL_TZ", "Europe/Berlin")

	row := runSettings(t, "get", "tz").row(t)
	if row.Value == nil || *row.Value != "Europe/Berlin" || row.Source == nil || *row.Source != "db" {
		t.Fatalf("seeded tz = %+v, want Europe/Berlin from the database", row)
	}
	if got := runSettings(t, "set", "tz", "America/Barbados"); got.code != cli.ExitOK {
		t.Fatalf("set tz: exit %d %q", got.code, got.stderr)
	}
	setEnv(t, "ATRIUM_INITIAL_TZ", "Asia/Tokyo")
	row = runSettings(t, "get", "tz").row(t)
	if row.Value == nil || *row.Value != "America/Barbados" || row.Source == nil || *row.Source != "db" {
		t.Errorf("existing tz = %+v, want America/Barbados to win over a new seed", row)
	}
}

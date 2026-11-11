package main

import (
	"net/http"
	"net/http/cookiejar"
	"net/url"
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

	for _, tc := range []struct{ key, value, want string }{
		{"tz", "Mars/Olympus", "must be an IANA time zone"},
		{"body_cap_bytes", "0", "must be between"},
		{"web_page_size", "9", "must be between"},
		{"snooze_choices", "1,x", "must be comma-separated"},
		{"media_qbittorrent_url", "http://127.0.0.1:8081", "is host configuration and is not editable here"},
		{"http_timeout_seconds", "3601", "must be between"},
	} {
		got := runSettings(t, "set", tc.key, tc.value)
		if got.code != cli.ExitError || got.stdout != "" || !strings.Contains(got.stderr, tc.want) {
			t.Errorf("set %s %s: exit %d stdout %q stderr %q, want %q", tc.key, tc.value, got.code, got.stdout, got.stderr, tc.want)
		}
	}
	if got := runSettings(t, "set", "tz", "America/Barbados"); got.code != cli.ExitOK {
		t.Fatalf("set tz: exit %d %q", got.code, got.stderr)
	}
	if got := runSettings(t, "set", "body_cap_bytes", "2048"); got.code != cli.ExitOK {
		t.Fatalf("set body_cap_bytes: exit %d %q", got.code, got.stderr)
	}

	base := startServeIn(t, dir)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if status, _, _ := fetch(t, client, "GET", base+"/chat", nil); status != http.StatusOK {
		t.Fatalf("GET /chat = %d", status)
	}
	post := func(note string) int {
		form := url.Values{"note": {note}}
		req, err := http.NewRequest("POST", base+"/chat", strings.NewReader(form.Encode()))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	if status := post(strings.Repeat("a", 4096)); status != http.StatusRequestEntityTooLarge {
		t.Errorf("POST above body_cap_bytes = %d, want 413", status)
	}
	if status := post("short"); status == http.StatusRequestEntityTooLarge {
		t.Errorf("POST below body_cap_bytes = %d, want it past the size check", status)
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

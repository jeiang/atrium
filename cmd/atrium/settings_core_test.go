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
	dir := t.TempDir()
	t.Setenv("ATRIUM_DB", filepath.Join(dir, "atrium.db"))
	t.Setenv("ATRIUM_CACHE_DIR", filepath.Join(dir, "cache"))
	t.Setenv("ATRIUM_PUBLIC_HOST", "")

	for _, tc := range []struct{ key, value string }{
		{"tz", "Mars/Olympus"},
		{"body_cap_bytes", "0"},
		{"page_size", "9"},
		{"snooze_choices", "1,x"},
		{"public_url", "ftp://atrium.test"},
		{"http_timeout_seconds", "3601"},
	} {
		if got := runSettings(t, "set", tc.key, tc.value); got.code != cli.ExitError || got.stdout != "" {
			t.Errorf("set %s %s: exit %d stdout %q, want a refusal", tc.key, tc.value, got.code, got.stdout)
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

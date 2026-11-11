package main

import (
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/passrun"
)

var tuningKeys = []string{
	"body_cap_bytes",
	"candidate_draft_limit",
	"candidate_scan_limit",
	"chat_thread_limit",
	"contacts_max_results",
	"http_timeout_seconds",
	"max_events",
	"max_occurrences",
	"pass_run_retention_days",
	"preview_bytes",
	"web_page_size",
	"web_run_history",
}

func TestTuningKeysAreAbsentFromSettings(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	setEnv(t, "ATRIUM_DB", filepath.Join(dir, "atrium.db"))
	setEnv(t, "ATRIUM_CACHE_DIR", filepath.Join(dir, "cache"))
	setEnv(t, "ATRIUM_PUBLIC_HOST", "")

	list := runSettings(t, "list")
	if list.code != cli.ExitOK {
		t.Fatalf("settings list: %d %s", list.code, list.stderr)
	}
	for _, key := range tuningKeys {
		if strings.Contains(list.stdout, `"`+key+`"`) {
			t.Errorf("settings list includes tuning key %s", key)
		}
		for _, args := range [][]string{{"get", key}, {"set", key, "1"}, {"unset", key}} {
			got := runSettings(t, args...)
			if got.code == cli.ExitOK || !strings.Contains(got.stderr, "compiled constant") {
				t.Errorf("settings %s: exit %d stderr %q, want a compiled-constant refusal", strings.Join(args, " "), got.code, got.stderr)
			}
		}
	}

	base := startServeIn(t, dir)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for _, path := range []string{"/settings/calendar", "/settings/assistant", "/settings/notion"} {
		status, body, _ := fetch(t, client, http.MethodGet, base+path, nil)
		if status != http.StatusOK {
			t.Fatalf("GET %s = %d", path, status)
		}
		for _, key := range tuningKeys {
			if strings.Contains(body, `name="`+key+`"`) {
				t.Errorf("%s renders tuning key %s", path, key)
			}
		}
	}
	status, body, _ := fetch(t, client, http.MethodPost, base+"/settings/sections/calendar", url.Values{
		"max_events": {"1"}, "max_occurrences": {"1"},
	})
	if status != http.StatusOK || !strings.Contains(body, "Saved") {
		t.Fatalf("POST /settings/sections/calendar = %d, want 200 with a saved toast: %.300s", status, body)
	}
	for _, key := range []string{"max_events", "max_occurrences"} {
		if _, ok := storedSetting(t, filepath.Join(dir, "atrium.db"), key); ok {
			t.Errorf("settings page stored tuning key %s", key)
		}
	}
}

func TestPoisonedTuningRowsCannotChangeHTTPBehavior(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	setEnv(t, "ATRIUM_DB", filepath.Join(dir, "atrium.db"))
	setEnv(t, "ATRIUM_CACHE_DIR", filepath.Join(dir, "cache"))
	setEnv(t, "ATRIUM_PUBLIC_HOST", "")
	if got := runSettings(t, "list"); got.code != cli.ExitOK {
		t.Fatalf("settings list: %d %s", got.code, got.stderr)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
	for _, row := range [][2]string{
		{"body_cap_bytes", "1024"},
		{"chat_thread_limit", "1"},
		{"http_timeout_seconds", "1"},
		{"preview_bytes", "1"},
		{"web_page_size", "1"},
		{"web_run_history", "1"},
	} {
		if _, err := d.Write.Exec("INSERT INTO settings (key, value) VALUES (?, ?)", row[0], row[1]); err != nil {
			t.Fatal(err)
		}
	}

	base := startServeIn(t, dir)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar}
	if status, _, _ := fetch(t, client, http.MethodGet, base+"/chat", nil); status != http.StatusOK {
		t.Fatalf("GET /chat = %d", status)
	}
	baseURL, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	token := ""
	for _, c := range jar.Cookies(baseURL) {
		if c.Name == "atrium_csrf" {
			token = c.Value
		}
	}
	if token == "" {
		t.Fatal("no CSRF cookie")
	}
	post := func(note string) int {
		form := url.Values{"csrf_token": {token}, "note": {note}}
		req, err := http.NewRequest(http.MethodPost, base+"/chat", strings.NewReader(form.Encode()))
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
	if status := post(strings.Repeat("a", 4096)); status == http.StatusRequestEntityTooLarge {
		t.Errorf("POST below the 1 MiB constant body cap = %d, want it past the size check", status)
	}
	if status := post(strings.Repeat("a", 1<<20)); status != http.StatusRequestEntityTooLarge {
		t.Errorf("POST above the 1 MiB constant body cap = %d, want 413", status)
	}
}

func TestPoisonedRetentionRowCannotChangePassPruning(t *testing.T) {
	t.Parallel()
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	if got := runApp(t, "settings", "list"); got.code != cli.ExitOK {
		t.Fatalf("settings list: %d %s", got.code, got.stderr)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
	if _, err := d.Write.Exec("INSERT INTO settings (key, value) VALUES ('pass_run_retention_days', '1')"); err != nil {
		t.Fatal(err)
	}
	old := float64(time.Now().Add(-91 * 24 * time.Hour).Unix())
	recent := float64(time.Now().Add(-48 * time.Hour).Unix())
	for _, at := range []float64{old, recent} {
		if _, err := d.Write.Exec("INSERT INTO pass_runs (job, started_at, finished_at, outcome) VALUES ('scan', ?, ?, 'ok')", at, at); err != nil {
			t.Fatal(err)
		}
	}

	if got := runApp(t, "proposals", "scan-all"); got.code != cli.ExitOK {
		t.Fatalf("scan-all: %d %s", got.code, got.stderr)
	}
	if ok, failed, _ := passRows(t, dir, passrun.Scan); ok != 2 || failed != 0 {
		t.Errorf("scan rows ok=%d failed=%d, want the new success and the 48h row kept while the 91d row is pruned by the 90-day constant", ok, failed)
	}
}

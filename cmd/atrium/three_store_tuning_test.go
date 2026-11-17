package main

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
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

	seedAllFlags(t)
	base := startServeIn(t, dir)
	web := localWebG5(t, base)
	for _, path := range []string{"/web/v1/settings", "/web/v1/settings/schema"} {
		got := web.do(http.MethodGet, path, nil)
		if got.status != http.StatusOK {
			t.Fatalf("GET %s = %d", path, got.status)
		}
		for _, key := range tuningKeys {
			if strings.Contains(got.body, `"`+key+`"`) {
				t.Errorf("%s exposes tuning key %s", path, key)
			}
		}
	}
	if got := web.do(http.MethodPatch, "/web/v1/settings", map[string]any{"set": map[string]string{"max_events": "1", "max_occurrences": "1"}}); got.status < 400 || got.status >= 500 {
		t.Fatalf("PATCH /web/v1/settings with tuning keys = %d, want a refusal: %.300s", got.status, got.body)
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
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), db.Key{})
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
	web := localWebG5(t, base)
	post := func(text string) int {
		return web.do(http.MethodPost, "/web/v1/threads", map[string]any{"text": text}).status
	}
	if status := post(strings.Repeat("a", 4096)); status == http.StatusRequestEntityTooLarge {
		t.Errorf("POST below the 1 MiB constant body cap = %d, want it past the size check", status)
	}
	if status := post(strings.Repeat("a", 1<<20)); status != http.StatusRequestEntityTooLarge {
		t.Errorf("POST above the 1 MiB constant body cap = %d, want 413", status)
	}
}

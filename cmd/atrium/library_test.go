package main

import (
	"fmt"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/apps/library"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/unit"
)

func librarySite(t *testing.T, flags map[string]string) (*apiSite, string, string) {
	t.Helper()
	dir, _, f := moduleFixture(t, flags)
	mnpMediaRoot(t, dir)
	_, qbURL := mnpNewQbit(t)
	setEnv(t, "ATRIUM_HOST_CONFIG", mediaHost(t, dir, qbURL, "", "", ""))
	token := localAPIToken(t, "api")
	base, _, stop, err := startServeAppArgs(t, f.app, &lockedBuffer{}, "--auth", "local")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	settleBootChecks(t, dir, keyFor(t))
	return &apiSite{t: t, base: base}, token, dir
}

func TestLibraryKeepsMediaHistoryListedAndUndoable(t *testing.T) {
	t.Parallel()
	s, token, dir := librarySite(t, flagsExcept("", ""))
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
	defer func() { _ = d.Close() }()
	id := proposalID(t, toolResult(t, "library_follow", map[string]any{"imdb_id": "tt1234567", "name": "Lanterns", "regex": "1080p"}))
	if got := runApp(t, "proposals", "approve", id); got.code != cli.ExitOK {
		t.Fatalf("approve %s: %d %s %s", id, got.code, got.stdout, got.stderr)
	}
	var effect int64
	var origin string
	if err := d.Read.QueryRow("SELECT id, origin FROM effects WHERE operation = 'media_channel_add' AND direction = 'forward'").Scan(&effect, &origin); err != nil {
		t.Fatalf("the follow did not record a media_channel_add effect: %v", err)
	}
	if origin != "media" {
		t.Fatalf("follow effect origin = %q, want the stored media origin", origin)
	}
	listed := apiData(t, s.do(http.MethodGet, "/api/v1/activity?category=library&limit=100", token, nil), http.StatusOK)
	var found map[string]any
	for _, raw := range apiItems(t, listed) {
		item := raw.(map[string]any)
		if ids, _ := item["effect_ids"].([]any); slices.ContainsFunc(ids, func(v any) bool { return number(t, v) == effect }) {
			found = item
		}
	}
	if found == nil || found["category"] != "library" || found["operation"] != "media_channel_add" {
		t.Fatalf("the media_channel_add effect is not listed under Library: %v in %v", found, listed)
	}
	if got := s.do(http.MethodPost, fmt.Sprintf("/api/v1/undo/write:%d", effect), token, nil); got.status != http.StatusOK {
		t.Fatalf("undo of the media_channel_add effect: %d %s", got.status, got.body)
	}
	var channels int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM media_channels").Scan(&channels); err != nil {
		t.Fatal(err)
	}
	if channels != 0 {
		t.Fatalf("%d channels after undo, want 0", channels)
	}

	if got := s.do(http.MethodPut, "/api/v1/notifications/channels/"+library.NotifyChannel, token, map[string]any{"content": "title"}); got.status >= 300 {
		t.Fatalf("set the media channel content: %d %s", got.status, got.body)
	}
	settings := apiBody(t, s.do(http.MethodGet, "/api/v1/notifications", token, nil), http.StatusOK)
	var channel map[string]any
	for _, raw := range settings["channels"].([]any) {
		if c := raw.(map[string]any); c["name"] == library.NotifyChannel {
			channel = c
		}
	}
	if channel["title"] != "Library" || channel["content"] != "title" {
		t.Fatalf("notification channel: %v in %v", channel, settings)
	}
	var stored string
	if err := d.Read.QueryRow("SELECT content FROM ntfy_channels WHERE name = 'media'").Scan(&stored); err != nil || stored != "title" {
		t.Fatalf("stored media channel content = %q, %v", stored, err)
	}
}

func TestLibraryShowsTorrentsOffAndRunsNothing(t *testing.T) {
	t.Parallel()
	s, token, dir := librarySite(t, flagsExcept(unit.Torrents, "false"))
	entry := appEntryOf(t, apiData(t, s.do(http.MethodGet, "/api/v1/apps", token, nil), http.StatusOK), unit.Media)
	if entry["name"] != "Library" || entry["state"] != "needs-settings" || !strings.Contains(fmt.Sprint(entry["reason"]), "needs torrents") || !strings.Contains(mustJSON(t, entry["unmet"]), `"torrents"`) {
		t.Fatalf("Library with Torrents off: %v", entry)
	}
	if got := s.do(http.MethodGet, "/api/v1/media/channels", token, nil); got.status != http.StatusNotFound {
		t.Fatalf("GET /api/v1/media/channels with Torrents off = %d: %s", got.status, got.body)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
	defer func() { _ = d.Close() }()
	for _, name := range []string{library.JobChannels, library.JobSweep} {
		job, _, _ := schedulerJob(t, d, name)
		got := s.do(http.MethodPost, fmt.Sprintf("/api/v1/schedules/%d/run", job), token, nil)
		if got.status >= 300 && got.status != http.StatusConflict {
			t.Fatalf("run-now of %s with Torrents off: %d %s", name, got.status, got.body)
		}
		if got.status < 300 {
			waitFor(t, func() bool {
				status, _ := schedulerStatus(t, d, name)
				return status == "skipped_module"
			})
		}
	}
}

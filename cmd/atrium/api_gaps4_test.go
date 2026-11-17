package main

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/apps/torrents"
	"github.com/jeiang/atrium/internal/cli"
)

func gaps4Site(t *testing.T) (*apiSite, string, *mnpQbit) {
	t.Helper()
	dir := t.TempDir()
	mnpMediaRoot(t, dir)
	qb, qbURL := mnpNewQbit(t)
	setEnv(t, "ATRIUM_HOST_CONFIG", mediaHost(t, dir, qbURL, "", "", ""))
	seedAllFlagsIn(t, dir)
	issued := runApp(t, "auth", "token", "issue", "--name", "api-gaps-4")
	if issued.code != cli.ExitOK {
		t.Fatalf("issue: %s", issued.stderr)
	}
	return &apiSite{t: t, base: startServeIn(t, dir)}, strings.TrimSpace(issued.stdout), qb
}

func gaps4Events(t *testing.T, s *apiSite, token string) <-chan appFrame {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, s.base+"/api/v1/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := (&http.Client{Timeout: 2 * time.Minute}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return appFrames(t, resp)
}

func TestTorrentControlUndoAndTheDownloadsTopic(t *testing.T) {
	const plain = "cccccccccccccccccccccccccccccccccccccccc"
	const other = "dddddddddddddddddddddddddddddddddddddddd"
	s, token, qb := gaps4Site(t)
	qb.put(mnpTorrent{hash: plain, name: "Plain.Thing", state: "downloading"})
	time.Sleep(2*torrents.WatchInterval + time.Second)
	if n := qb.listed(); n != 0 {
		t.Fatalf("qBittorrent was listed %d times with no event subscriber", n)
	}
	frames := gaps4Events(t, s, token)
	synced := syncedRevisions(t, nextAppFrame(t, frames, "sync"))
	revision, ok := synced["downloads"]
	if !ok {
		t.Fatalf("sync has no downloads topic: %v", synced)
	}

	for deadline := time.Now().Add(10 * time.Second); qb.listed() == 0; time.Sleep(100 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("qBittorrent was not listed after an event subscriber connected")
		}
	}
	qb.put(mnpTorrent{hash: other, name: "Other.Thing", state: "downloading"})
	revision = waitAppChange(t, frames, "downloads", revision)

	control := func(action string) apiReply {
		return s.do(http.MethodPost, "/api/v1/media/torrents/"+plain+"/control", token, map[string]any{"action": action})
	}
	undoableWrite(t, s, token, control("pause"), http.StatusOK)
	if qb.count("torrents/stop "+plain) != 1 || qb.count("torrents/start "+plain) != 1 {
		t.Fatalf("pause undo did not resume the torrent: %v", qb.writes)
	}
	revision = waitAppChange(t, frames, "downloads", revision)

	again := control("resume")
	apiData(t, again, http.StatusOK)
	if effects, _ := again.json["effect_ids"].([]any); len(effects) != 1 || again.json["undoable"] != false {
		t.Fatalf("resume of a running torrent: %s", again.body)
	}
	expectAPI(t, control("pause"), http.StatusOK, "")
	undoableWrite(t, s, token, control("resume"), http.StatusOK)
	if qb.count("torrents/stop "+plain) != 3 {
		t.Fatalf("resume undo did not pause the torrent: %v", qb.writes)
	}

	removed := control("remove")
	apiData(t, removed, http.StatusOK)
	effects, _ := removed.json["effect_ids"].([]any)
	if len(effects) != 1 || removed.json["undoable"] != false {
		t.Fatalf("remove is reported undoable: %s", removed.body)
	}
	if undo := s.do(http.MethodPost, fmt.Sprintf("/api/v1/undo/write:%d", number(t, effects[0])), token, nil); undo.status == http.StatusOK {
		t.Fatalf("a removal was undone: %s", undo.body)
	}
	waitAppChange(t, frames, "downloads", revision)
}

func TestMediaChannelOperatorUndoKeepsTheChannelID(t *testing.T) {
	s, token, _ := gaps4Site(t)
	add := func(name string) apiReply {
		return s.do(http.MethodPost, "/api/v1/media/channels", token, map[string]any{"name": name, "imdb": "tt1234567", "pattern": "1080p"})
	}
	channels := func() []any {
		got := s.do(http.MethodGet, "/api/v1/media/channels", token, nil)
		expectAPI(t, got, http.StatusOK, "")
		items, _ := got.json["items"].([]any)
		return items
	}
	undoableWrite(t, s, token, add("Lanterns"), http.StatusOK)
	if left := channels(); len(left) != 0 {
		t.Fatalf("undone add left channels: %v", left)
	}

	kept := add("Beacons")
	apiData(t, kept, http.StatusOK)
	id := number(t, kept.json["channel"].(map[string]any)["id"])
	undoableWrite(t, s, token, s.do(http.MethodDelete, fmt.Sprintf("/api/v1/media/channels/%d", id), token, nil), http.StatusOK)
	left := channels()
	if len(left) != 1 || number(t, left[0].(map[string]any)["id"]) != id || left[0].(map[string]any)["name"] != "Beacons" {
		t.Fatalf("undone removal did not restore channel %d: %v", id, left)
	}
}

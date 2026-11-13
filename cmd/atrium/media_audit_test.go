package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/media"
)

func mediaAudit(t *testing.T, d *db.DB, operation, want string) map[string]int {
	t.Helper()
	var outcome, details string
	if err := d.Read.QueryRow("SELECT outcome, details_json FROM operation_audit WHERE operation = ? ORDER BY id DESC LIMIT 1", operation).Scan(&outcome, &details); err != nil {
		t.Fatal(err)
	}
	if outcome != want {
		t.Fatalf("%s audit outcome %s, want %s (%s)", operation, outcome, want, details)
	}
	var counts map[string]int
	if err := json.Unmarshal([]byte(details), &counts); err != nil {
		t.Fatal(err)
	}
	return counts
}

func TestMediaEmptyPollAndSweepAuditNoop(t *testing.T) {
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	mnpMediaRoot(t, dir)
	_, qbURL := mnpNewQbit(t)
	setEnv(t, "ATRIUM_HOST_CONFIG", mediaHost(t, dir, qbURL, "", "", ""))
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
	defer func() { _ = d.Close() }()
	for _, command := range []string{"poll", "sweep"} {
		if got := runApp(t, "media", command); got.code != cli.ExitOK {
			t.Fatalf("%s: %d %s", command, got.code, got.stderr)
		}
		mediaAudit(t, d, "media."+command, "noop")
	}
}

func TestMediaAddSelectionDatabaseFailureAuditsPartial(t *testing.T) {
	dir := t.TempDir()
	mnpMediaRoot(t, dir)
	qb, qbURL := mnpNewQbit(t)
	setEnv(t, "ATRIUM_HOST_CONFIG", mediaHost(t, dir, qbURL, "", "", ""))
	qb.put(mnpTorrent{hash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", name: "Dune.2021", tags: []string{media.TagMedia}, state: "stoppedDL", content: "Dune.2021", files: []mnpFile{{0, "Dune.2021.mkv", 0}}})
	seedAllFlagsIn(t, dir)
	b := mnpNewBrowser(t, startServeIn(t, dir))
	b.want(http.MethodGet, "/apps/media/downloads", nil, http.StatusOK)
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
	defer func() { _ = d.Close() }()
	if _, err := d.Write.Exec("CREATE TRIGGER refuse_priority BEFORE INSERT ON effects WHEN NEW.operation = 'media_file_priority' BEGIN SELECT RAISE(FAIL, 'unavailable'); END"); err != nil {
		t.Fatal(err)
	}
	const hash = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	b.want(http.MethodPost, "/apps/media/add", url.Values{"magnet": {mediaMagnet(hash, "Lanterns")}, "mode": {"media"}}, http.StatusBadGateway)
	mediaEffectID(t, d, media.OpAddMagnet, hash, "done")
	var outcome string
	if err := d.Read.QueryRow("SELECT outcome FROM operation_audit WHERE operation = 'media.add' ORDER BY id DESC LIMIT 1").Scan(&outcome); err != nil {
		t.Fatal(err)
	}
	if outcome != "partial" || qb.count("add") != 1 {
		t.Fatalf("add outcome %s, adds %d", outcome, qb.count("add"))
	}
}

func TestMediaSavedChannelAndFailedPollKeepCommittedAdds(t *testing.T) {
	const hash = "cccccccccccccccccccccccccccccccccccccccc"
	feed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"torrents": []map[string]any{{"hash": hash, "title": "Lanterns S01E01 1080p", "magnet_url": mediaMagnet(hash, "Lanterns"), "size_bytes": "1000", "season": "1", "episode": "1"}}})
	}))
	defer feed.Close()
	dir := t.TempDir()
	mnpMediaRoot(t, dir)
	qb, qbURL := mnpNewQbit(t)
	setEnv(t, "ATRIUM_HOST_CONFIG", mediaHost(t, dir, qbURL, feed.URL+"/api/get-torrents", "", ""))
	seedAllFlagsIn(t, dir)
	b := mnpNewBrowser(t, startServeIn(t, dir))
	b.want(http.MethodGet, "/apps/media/channels", nil, http.StatusOK)
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
	defer func() { _ = d.Close() }()
	if _, err := d.Write.Exec("CREATE TRIGGER refuse_prime BEFORE UPDATE ON media_channels BEGIN SELECT RAISE(FAIL, 'unavailable'); END"); err != nil {
		t.Fatal(err)
	}
	b.want(http.MethodPost, "/apps/media/channels", url.Values{"name": {"Lanterns"}, "imdb": {"tt1234567"}, "pattern": {"1080p"}, "backfill": {"1"}}, http.StatusOK)
	var channels int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM media_channels WHERE name = 'Lanterns'").Scan(&channels); err != nil {
		t.Fatal(err)
	}
	if channels != 1 || qb.count("add") != 1 {
		t.Fatalf("saved channels %d, adds %d", channels, qb.count("add"))
	}
	if counts := mediaAudit(t, d, "media.poll", "partial"); counts["added"] != 1 {
		t.Fatalf("poll counts %v", counts)
	}
	mediaEffectID(t, d, media.OpAddMagnet, hash, "done")
	if _, err := d.Write.Exec("DROP TRIGGER refuse_prime; UPDATE media_channels SET primed = 1; DELETE FROM media_seen; CREATE TRIGGER refuse_prime BEFORE UPDATE ON media_channels BEGIN SELECT RAISE(FAIL, 'unavailable'); END"); err != nil {
		t.Fatal(err)
	}
	got := runApp(t, "media", "poll")
	if got.code == cli.ExitOK {
		t.Fatal("failed poll exited successfully")
	}
	var result struct {
		Added []struct {
			Hash string `json:"hash"`
		} `json:"added"`
	}
	if err := json.Unmarshal([]byte(got.stdout), &result); err != nil {
		t.Fatalf("poll result: %v (%s)", err, got.stdout)
	}
	if len(result.Added) != 1 || result.Added[0].Hash != hash {
		t.Fatalf("poll result %s", got.stdout)
	}
	if counts := mediaAudit(t, d, "media.poll", "partial"); counts["added"] != 1 {
		t.Fatalf("poll counts %v", counts)
	}
}

func TestMediaSweepSelectionThenDeleteFailureAuditsPartial(t *testing.T) {
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	mnpMediaRoot(t, dir)
	qb, qbURL := mnpNewQbit(t)
	setEnv(t, "ATRIUM_HOST_CONFIG", mediaHost(t, dir, qbURL, "", "", ""))
	qb.put(mnpTorrent{hash: "dddddddddddddddddddddddddddddddddddddddd", name: "Dune.2021", tags: []string{media.TagMedia}, state: "stoppedDL", files: []mnpFile{{0, "Dune.2021.mkv", 0}}})
	qb.put(mnpTorrent{hash: "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", name: "Filed", tags: []string{media.TagMedia, media.TagSelected, media.TagFiled}, state: media.StoppedUP, progress: 1})
	qb.dropOnce("torrents/delete")
	if got := runApp(t, "media", "sweep"); got.code != cli.ExitOK {
		t.Fatalf("sweep: %d %s", got.code, got.stderr)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
	defer func() { _ = d.Close() }()
	counts := mediaAudit(t, d, "media.sweep", "partial")
	if counts["selected"] != 1 || counts["removed"] != 0 || counts["errors"] != 1 {
		t.Fatalf("sweep counts %v", counts)
	}
}

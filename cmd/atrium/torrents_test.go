package main

import (
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/apps/library"
	"github.com/jeiang/atrium/internal/apps/torrents/contract"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/unit"
)

func TestTorrentsIntakeKeysAgentWritesToItsOwnTags(t *testing.T) {
	t.Parallel()
	const (
		legacy  = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		stamped = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		plain   = "cccccccccccccccccccccccccccccccccccccccc"
		fresh   = "dddddddddddddddddddddddddddddddddddddddd"
	)
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	mnpMediaRoot(t, dir)
	qb, qbURL := mnpNewQbit(t)
	setEnv(t, "ATRIUM_HOST_CONFIG", mediaHost(t, dir, qbURL, "", "", ""))
	qb.put(mnpTorrent{hash: legacy, name: "Legacy.Show", tags: []string{library.TagMedia}, state: "downloading"})
	qb.put(mnpTorrent{hash: stamped, name: "Stamped.Thing", tags: []string{contract.TagAtrium}, state: "downloading"})
	qb.put(mnpTorrent{hash: plain, name: "Plain.Thing", state: contract.StoppedUP, progress: 1})

	listed := unescape(toolResult(t, "downloads", map[string]any{}))
	for _, name := range []string{"Legacy.Show", "Stamped.Thing"} {
		if !strings.Contains(listed, name) {
			t.Fatalf("downloads did not list %s: %s", name, listed)
		}
	}

	pause := proposalID(t, toolResult(t, "download_control", map[string]any{"hash": legacy, "action": "pause"}))
	if qb.count("torrents/stop") != 0 {
		t.Fatal("asking to pause reached qBittorrent")
	}
	if got := runApp(t, "proposals", "approve", pause); got.code != cli.ExitOK {
		t.Fatalf("approve pause: %d %q %q", got.code, got.stdout, got.stderr)
	}
	if qb.count("torrents/stop "+legacy) != 1 {
		t.Fatalf("pausing the legacy-tagged torrent sent %v", qb.writes)
	}
	resume := proposalID(t, toolResult(t, "download_control", map[string]any{"hash": stamped, "action": "resume"}))
	if got := runApp(t, "proposals", "approve", resume); got.code != cli.ExitOK {
		t.Fatalf("approve resume: %d %q %q", got.code, got.stdout, got.stderr)
	}
	if qb.count("torrents/start "+stamped) != 1 {
		t.Fatalf("resuming the atrium-tagged torrent sent %v", qb.writes)
	}

	removal := unescape(toolResult(t, "download_control", map[string]any{"hash": plain, "action": "remove"}))
	if strings.Contains(removal, "proposal_id") {
		_ = runApp(t, "proposals", "approve", proposalID(t, removal))
	}
	if qb.touched(plain) || qb.count("delete") != 0 {
		t.Fatalf("an agent removed an untagged torrent: %s writes %v", removal, qb.writes)
	}

	add := proposalID(t, toolResult(t, "download_add", map[string]any{"magnet": mediaMagnet(fresh, "Lanterns.S01E13")}))
	if got := runApp(t, "proposals", "approve", add); got.code != cli.ExitOK {
		t.Fatalf("approve add: %d %q %q", got.code, got.stdout, got.stderr)
	}
	if got := qb.lastAdd(); got.Get("tags") != contract.TagAtrium+","+library.TagMedia {
		t.Fatalf("agent add sent tags %q, want atrium and the Library label", got.Get("tags"))
	}

	for _, hash := range []string{legacy, stamped} {
		if tags, _ := qb.state(hash); len(tags) != 1 {
			t.Fatalf("tags on %s changed to %v", hash, tags)
		}
	}
	if qb.count("torrents/removeTags") != 0 {
		t.Fatalf("a tag was removed: %v", qb.writes)
	}
}

func TestTorrentsOperatorRemovesAnyTorrentThroughTheAPI(t *testing.T) {
	t.Parallel()
	const plain = "cccccccccccccccccccccccccccccccccccccccc"
	dir := t.TempDir()
	mnpMediaRoot(t, dir)
	qb, qbURL := mnpNewQbit(t)
	setEnv(t, "ATRIUM_HOST_CONFIG", mediaHost(t, dir, qbURL, "", "", ""))
	qb.put(mnpTorrent{hash: plain, name: "Plain.Thing", state: "downloading"})
	seedAllFlagsIn(t, dir)
	issued := runApp(t, "auth", "token", "issue", "--name", "torrents-api")
	if issued.code != cli.ExitOK {
		t.Fatalf("issue: %s", issued.stderr)
	}
	token := strings.TrimSpace(issued.stdout)
	s := &apiSite{t: t, base: startServeIn(t, dir)}
	expectAPI(t, s.do(http.MethodPost, "/api/v1/media/torrents/"+plain+"/control", token, map[string]any{"action": "pause"}), 200, "")
	if qb.count("torrents/stop "+plain) != 1 {
		t.Fatalf("operator pause sent %v", qb.writes)
	}
	expectAPI(t, s.do(http.MethodPost, "/api/v1/media/torrents/"+plain+"/control", token, map[string]any{"action": "remove"}), 200, "")
	if qb.count("delete") != 1 {
		t.Fatalf("operator remove of an untagged torrent sent %v", qb.writes)
	}
}

func TestTorrentsShimAddLandsOnDownloadsWhileLibraryIsOn(t *testing.T) {
	t.Parallel()
	const fresh = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	dir := t.TempDir()
	mnpMediaRoot(t, dir)
	qb, qbURL := mnpNewQbit(t)
	setEnv(t, "ATRIUM_HOST_CONFIG", mediaHost(t, dir, qbURL, "", "", ""))
	seedAllFlagsIn(t, dir)
	base := startServeIn(t, dir)
	b := localWebG5(t, base)
	magnet := mediaMagnet(fresh, "Lanterns.S01E14")
	status, _, header := noFollowG5(t, http.MethodGet, base+"/apps/media/add?magnet="+url.QueryEscape(magnet))
	if status != http.StatusMovedPermanently || !strings.HasPrefix(header.Get("Location"), "/apps/torrents") || !strings.Contains(header.Get("Location"), fresh) {
		t.Fatalf("shim link = %d %q, want a redirect to the torrents app carrying the magnet", status, header.Get("Location"))
	}
	if qb.count("add") != 0 {
		t.Fatal("opening the shim link added a torrent")
	}
	if got := b.do(http.MethodPost, "/web/v1/media/torrents", map[string]any{"magnet": magnet}); got.status != http.StatusOK || !strings.Contains(got.body, fresh) {
		t.Fatalf("add = %d %.300s", got.status, got.body)
	}
	if got := qb.lastAdd(); got.Get("tags") != contract.TagAtrium+","+library.TagMedia {
		t.Fatalf("shim add sent tags %q", got.Get("tags"))
	}
}

func TestTorrentsAddWithoutLibraryStaysOnTheAddPage(t *testing.T) {
	t.Parallel()
	const fresh = "ffffffffffffffffffffffffffffffffffffffff"
	dir, _, _ := moduleFixture(t, flagsExcept(unit.Media, "false"))
	mnpMediaRoot(t, dir)
	qb, qbURL := mnpNewQbit(t)
	setEnv(t, "ATRIUM_HOST_CONFIG", mediaHost(t, dir, qbURL, "", "", ""))
	b := localWebG5(t, startServeIn(t, dir))
	if got := b.do(http.MethodGet, "/web/v1/media/library", nil); got.status != http.StatusNotFound {
		t.Fatalf("library while Library is off = %d %.300s", got.status, got.body)
	}
	if got := b.do(http.MethodPost, "/web/v1/media/torrents", map[string]any{"magnet": mediaMagnet(fresh, "Lanterns.S01E15")}); got.status != http.StatusOK {
		t.Fatalf("add without Library = %d %.300s", got.status, got.body)
	}
	if got := qb.lastAdd(); got.Get("tags") != contract.TagAtrium || qb.count("torrents/filePrio") != 0 {
		t.Fatalf("add without Library sent tags %q writes %v", got.Get("tags"), qb.writes)
	}
}

func TestTorrentsFlagFollowsLibraryOnceAtSeed(t *testing.T) {
	t.Parallel()
	flag := module.FlagKey(unit.Torrents)
	rows := func(t *testing.T, dir string) (settings, audits, migrated string) {
		t.Helper()
		d := mustConnect(t, filepath.Join(dir, "atrium.db"), db.Key{})
		defer func() { _ = d.Close() }()
		return queryValue(t, d.Read, "SELECT COUNT(*) FROM settings WHERE key = ?", flag),
			queryValue(t, d.Read, "SELECT COUNT(*) FROM settings_audit WHERE key = ?", flag),
			queryValue(t, d.Read, "SELECT COUNT(*) FROM settings_audit WHERE key = ? AND new_value = 'true' AND source = 'migration' AND actor = 'atrium'", flag)
	}
	t.Run("Library on writes the flag once", func(t *testing.T) {
		dir, _, _ := moduleFixture(t, map[string]string{unit.Media: "true"})
		startServeIn(t, dir)
		if settings, audits, migrated := rows(t, dir); settings != "1" || audits != "1" || migrated != "1" {
			t.Fatalf("rows %s, audits %s, migration audits %s, want one of each", settings, audits, migrated)
		}
	})
	t.Run("a fresh database stays opt-in", func(t *testing.T) {
		dir, _, _ := moduleFixture(t, map[string]string{})
		startServeIn(t, dir)
		if settings, audits, _ := rows(t, dir); settings != "0" || audits != "0" {
			t.Fatalf("a fresh database wrote %s rows and %s audits", settings, audits)
		}
	})
	t.Run("Library off writes nothing", func(t *testing.T) {
		dir, _, _ := moduleFixture(t, map[string]string{unit.Media: "false"})
		startServeIn(t, dir)
		if settings, audits, _ := rows(t, dir); settings != "0" || audits != "0" {
			t.Fatalf("Library off wrote %s rows and %s audits", settings, audits)
		}
	})
}

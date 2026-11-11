package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/media"
)

func mediaEffectID(t *testing.T, d *db.DB, operation, target, state string) int64 {
	t.Helper()
	var id int64
	err := d.Read.QueryRow(
		"SELECT id FROM effects WHERE direction = 'forward' AND operation = ? AND target = ? AND state = ? ORDER BY id DESC LIMIT 1",
		operation, target, state).Scan(&id)
	if err != nil {
		t.Fatalf("no %s effect for %s in state %s: %v", operation, target, state, err)
	}
	return id
}

func mediaEffectCount(t *testing.T, d *db.DB, operation, target string) int {
	t.Helper()
	var n int
	if err := d.Read.QueryRow(
		"SELECT COUNT(*) FROM effects WHERE direction = 'forward' AND operation = ? AND target = ?", operation, target).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func mediaMagnet(hash, name string) string {
	return "magnet:?xt=urn:btih:" + hash + "&dn=" + name
}

func TestMediaUnknownAddBlocksWebAndPoll(t *testing.T) {
	t.Parallel()
	const hash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	dir := t.TempDir()
	mnpMediaRoot(t, dir)
	qb, qbURL := mnpNewQbit(t)
	setEnv(t, settingEnv(media.KeyQbitURL), qbURL)
	magnet := mediaMagnet(hash, "Lanterns.S01E01")
	eztv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"torrents": []map[string]any{{
			"hash": hash, "title": "Lanterns S01E01 1080p WEB", "magnet_url": magnet,
			"size_bytes": "1000000", "season": "1", "episode": "1",
		}}})
	}))
	t.Cleanup(eztv.Close)
	setEnv(t, settingEnv(media.KeyEZTVURL), eztv.URL+"/api/get-torrents")

	qb.dropOnce("torrents/add")
	b := mnpNewBrowser(t, startServeIn(t, dir))
	b.want(http.MethodGet, "/apps/media/downloads", nil, http.StatusOK)
	if code, body := b.do(http.MethodPost, "/apps/media/add", url.Values{"magnet": {magnet}, "mode": {"media"}}); code != http.StatusBadGateway {
		t.Fatalf("dropped add = %d, want 502: %.400s", code, body)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
	defer func() { _ = d.Close() }()
	id := mediaEffectID(t, d, media.OpAddMagnet, hash, "unknown")
	if adds := qb.count("add"); adds != 1 {
		t.Fatalf("the dropped add reached qBittorrent %d times, want 1", adds)
	}
	if code, body := b.do(http.MethodPost, "/apps/media/add", url.Values{"magnet": {magnet}, "mode": {"media"}}); code != http.StatusBadGateway {
		t.Fatalf("blocked add = %d, want 502: %.400s", code, body)
	}
	if adds := qb.count("add"); adds != 1 {
		t.Fatalf("a blocked add reached qBittorrent: %d adds", adds)
	}
	body := b.want(http.MethodPost, "/apps/media/channels",
		url.Values{"name": {"Lanterns"}, "imdb": {"tt1234567"}, "pattern": {"1080p"}, "backfill": {"1"}},
		http.StatusOK, "unresolved")
	if !strings.Contains(body, "unresolved") {
		t.Fatalf("the forced poll did not report the blocked infohash: %.400s", body)
	}
	if adds := qb.count("add"); adds != 1 {
		t.Fatalf("the blocked poll reached qBittorrent: %d adds", adds)
	}
	if ran := runApp(t, "effects", "resolve", fmt.Sprint(id), "--outcome", "not-applied"); ran.code != cli.ExitOK {
		t.Fatalf("effects resolve: %d %s", ran.code, ran.stderr)
	}
	if ran := runApp(t, "media", "poll"); ran.code != cli.ExitOK {
		t.Fatalf("media poll after resolution: %d %s", ran.code, ran.stderr)
	}
	if adds := qb.count("add"); adds != 2 {
		t.Fatalf("the poll did not retry the released infohash: %d adds", adds)
	}
	mediaEffectID(t, d, media.OpAddMagnet, hash, "done")
}

func TestMediaSelectionFailureKeepsTheAddAndBlocksTheRepeat(t *testing.T) {
	t.Parallel()
	const (
		busy  = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		fresh = "cccccccccccccccccccccccccccccccccccccccc"
	)
	dir := t.TempDir()
	mnpMediaRoot(t, dir)
	qb, qbURL := mnpNewQbit(t)
	setEnv(t, settingEnv(media.KeyQbitURL), qbURL)
	qb.put(mnpTorrent{hash: busy, name: "Dune.2021", tags: []string{media.TagMedia}, state: "stoppedDL", content: "Dune.2021",
		files: []mnpFile{{0, "Dune.2021.mkv", 0}, {1, "Dune.2021.nfo", 1}}})
	qb.dropOnce("torrents/filePrio")

	b := mnpNewBrowser(t, startServeIn(t, dir))
	b.want(http.MethodGet, "/apps/media/downloads", nil, http.StatusOK)
	b.want(http.MethodPost, "/apps/media/add", url.Values{"magnet": {mediaMagnet(fresh, "Lanterns.S01E09")}, "mode": {"media"}},
		http.StatusOK, "Added the magnet link")
	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
	defer func() { _ = d.Close() }()
	mediaEffectID(t, d, media.OpAddMagnet, fresh, "done")
	priority := mediaEffectID(t, d, media.OpPriority, busy, "unknown")
	if writes := qb.count("torrents/filePrio"); writes != 1 {
		t.Fatalf("the dropped priority reached qBittorrent %d times, want 1", writes)
	}
	if ran := runApp(t, "media", "sweep"); ran.code != cli.ExitOK {
		t.Fatalf("sweep with a blocked selection: %d %s", ran.code, ran.stderr)
	}
	if writes := qb.count("torrents/filePrio"); writes != 1 {
		t.Fatalf("an automatic sweep repeated the blocked priority: %d writes", writes)
	}
	if ran := runApp(t, "effects", "resolve", fmt.Sprint(priority), "--outcome", "not-applied"); ran.code != cli.ExitOK {
		t.Fatalf("effects resolve: %d %s", ran.code, ran.stderr)
	}
	if ran := runApp(t, "media", "sweep"); ran.code != cli.ExitOK {
		t.Fatalf("sweep after resolution: %d %s", ran.code, ran.stderr)
	}
	if writes := qb.count("torrents/filePrio"); writes < 2 {
		t.Fatalf("the selection did not resume after resolution: %d writes", writes)
	}
	if tags, _ := qb.state(busy); !slices.Contains(tags, media.TagSelected) {
		t.Fatalf("the selected torrent tags %v", tags)
	}
}

func TestMediaSweepDeleteDropDoesNotRepeat(t *testing.T) {
	t.Parallel()
	const hash = "dddddddddddddddddddddddddddddddddddddddd"
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	mnpMediaRoot(t, dir)
	qb, qbURL := mnpNewQbit(t)
	setEnv(t, settingEnv(media.KeyQbitURL), qbURL)
	qb.put(mnpTorrent{hash: hash, name: "Filed.Show", tags: []string{media.TagMedia, media.TagFiled}, state: media.StoppedUP, progress: 1})
	qb.dropOnce("torrents/delete")

	ran := runApp(t, "media", "sweep")
	if ran.code != cli.ExitOK {
		t.Fatalf("sweep: %d %s", ran.code, ran.stderr)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
	defer func() { _ = d.Close() }()
	id := mediaEffectID(t, d, media.OpDelete, hash, "unknown")
	if deletes := qb.count("delete"); deletes != 1 {
		t.Fatalf("the dropped delete reached qBittorrent %d times, want 1", deletes)
	}
	if ran := runApp(t, "media", "sweep"); ran.code != cli.ExitOK {
		t.Fatalf("second sweep: %d %s", ran.code, ran.stderr)
	}
	if deletes := qb.count("delete"); deletes != 1 {
		t.Fatalf("the sweep repeated the delete: %d deletes", deletes)
	}
	if ran := runApp(t, "effects", "resolve", fmt.Sprint(id), "--outcome", "applied"); ran.code != cli.ExitOK {
		t.Fatalf("effects resolve: %d %s", ran.code, ran.stderr)
	}
	var outcome string
	if err := d.Read.QueryRow("SELECT effective_outcome FROM effect_resolutions WHERE effect_id = ? ORDER BY id DESC LIMIT 1", id).Scan(&outcome); err != nil {
		t.Fatal(err)
	}
	if outcome != "done" {
		t.Fatalf("the delete resolution is %q, want done", outcome)
	}
}

func TestMediaFilingPlanIsInspectableAndUndoable(t *testing.T) {
	t.Parallel()
	const hash = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	name := "Lanterns.S01E01.S01E02.1080p.WEB"
	dir := t.TempDir()
	root := mnpMediaRoot(t, dir)
	qb, qbURL := mnpNewQbit(t)
	setEnv(t, settingEnv(media.KeyQbitURL), qbURL)
	content := filepath.Join(root, "downloads", name)
	mnpWrite(t, filepath.Join(content, "Lanterns.S01E01.mkv"), []byte("one"))
	mnpWrite(t, filepath.Join(content, "Lanterns.S01E02.mkv"), []byte("two"))
	qb.put(mnpTorrent{hash: hash, name: name, tags: []string{media.TagMedia}, state: media.StoppedUP, progress: 1, content: content})
	if err := os.MkdirAll(filepath.Join(root, "tv", "Lanterns (2026)"), 0o775); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "tv", "Lanterns (2026)", "Season 02"), []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}

	b := mnpNewBrowser(t, startServeIn(t, dir))
	b.want(http.MethodGet, "/apps/media/downloads", nil, http.StatusOK)
	form := url.Values{"id": {hash}, "action": {"file"}, "kind": {"tv"}, "title": {"Lanterns"}, "year": {"2026"},
		"s0": {"1"}, "e0": {"1"}, "s1": {"2"}, "e1": {"1"}}
	if code, body := b.do(http.MethodPost, "/apps/media/file", form); code != http.StatusBadGateway {
		t.Fatalf("filing with a blocked second placement = %d, want 502: %.400s", code, body)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
	defer func() { _ = d.Close() }()
	first := mediaEffectID(t, d, media.OpFile, hash, "done")
	mediaEffectID(t, d, media.OpFile, hash, "failed")
	shown := runApp(t, "effects", "show", fmt.Sprint(first))
	if shown.code != cli.ExitOK {
		t.Fatalf("effects show: %d %s", shown.code, shown.stderr)
	}
	for _, dst := range []string{"Lanterns (2026) - S01E01.mkv", "Lanterns (2026) - S02E01.mkv"} {
		if !strings.Contains(shown.stdout, dst) {
			t.Errorf("the stored plan misses %s: %s", dst, shown.stdout)
		}
	}
	placed := filepath.Join(root, "tv", "Lanterns (2026)", "Season 01", "Lanterns (2026) - S01E01.mkv")
	src := filepath.Join(content, "Lanterns.S01E01.mkv")
	link, err := os.Stat(placed)
	if err != nil {
		t.Fatal(err)
	}
	original, err := os.Stat(src)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(link, original) {
		t.Fatal("the first placement is not a hardlink to the download")
	}
	if ran := runApp(t, "effects", "undo", fmt.Sprint(first)); ran.code != cli.ExitOK {
		t.Fatalf("effects undo: %d %s", ran.code, ran.stderr)
	}
	if _, err := os.Stat(placed); !os.IsNotExist(err) {
		t.Fatalf("the undo left the filed link: %v", err)
	}
	if _, err := os.Stat(src); err != nil {
		t.Fatalf("the undo removed the download: %v", err)
	}
}

func TestMediaFilingUndoRefusesAChangedDestination(t *testing.T) {
	t.Parallel()
	name := "Movie.2020"
	dir := t.TempDir()
	root := mnpMediaRoot(t, dir)
	_, qbURL := mnpNewQbit(t)
	setEnv(t, settingEnv(media.KeyQbitURL), qbURL)
	src := filepath.Join(root, "import", name, name+".mkv")
	mnpWrite(t, src, []byte("movie bytes"))
	item := media.Import + "/" + name

	b := mnpNewBrowser(t, startServeIn(t, dir))
	b.want(http.MethodGet, "/apps/media/downloads", nil, http.StatusOK)
	form := url.Values{"id": {item}, "action": {"file"}, "kind": {"movie"}, "title": {"Movie"}, "year": {"2020"}}
	b.want(http.MethodPost, "/apps/media/file", form, http.StatusOK, "Filed "+name)
	placed := filepath.Join(root, "movies", "Movie (2020)", "Movie (2020).mkv")
	if data, err := os.ReadFile(placed); err != nil || string(data) != "movie bytes" {
		t.Fatalf("filed movie = %q, %v", data, err)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatalf("the import source survived the rename: %v", err)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
	defer func() { _ = d.Close() }()
	id := mediaEffectID(t, d, media.OpFile, item, "done")
	if err := os.Remove(placed); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(placed, []byte("someone else"), 0o644); err != nil {
		t.Fatal(err)
	}
	if ran := runApp(t, "effects", "undo", fmt.Sprint(id)); ran.code == cli.ExitOK {
		t.Fatalf("the undo removed a destination it did not create: %s", ran.stdout)
	}
	if data, err := os.ReadFile(placed); err != nil || string(data) != "someone else" {
		t.Fatalf("the refused undo touched the destination: %q, %v", data, err)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatalf("the refused undo restored the source: %v", err)
	}
}

func TestApprovedMediaAddIsUndoneWithItsProposal(t *testing.T) {
	const hash = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	mnpMediaRoot(t, dir)
	qb, qbURL := mnpNewQbit(t)
	setEnv(t, settingEnv(media.KeyQbitURL), qbURL)
	id := proposalID(t, toolResult(t, "media_add_magnet", map[string]any{"magnet": mediaMagnet(hash, "Lanterns.S01E10")}))
	if adds := qb.count("add"); adds != 0 {
		t.Fatalf("asking reached qBittorrent: %d adds", adds)
	}
	if got := runApp(t, "proposals", "approve", id); got.code != cli.ExitOK {
		t.Fatalf("approve: %d %q %q", got.code, got.stdout, got.stderr)
	}
	if adds := qb.count("add"); adds != 1 {
		t.Fatalf("%d adds after approval, want 1", adds)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
	defer func() { _ = d.Close() }()
	var recorded int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM effects WHERE proposal_id = ? AND operation = ? AND direction = 'forward'",
		id, media.OpAddMagnet).Scan(&recorded); err != nil {
		t.Fatal(err)
	}
	if recorded != 1 {
		t.Fatalf("%d approved adds carry the proposal id, want 1", recorded)
	}
	if got := runApp(t, "proposals", "undo", id); got.code != cli.ExitOK {
		t.Fatalf("proposals undo: %d %q %q", got.code, got.stdout, got.stderr)
	}
	if deletes := qb.count("delete"); deletes != 1 {
		t.Fatalf("%d torrent deletes after the proposal undo, want 1", deletes)
	}
	var undone *float64
	if err := d.Read.QueryRow("SELECT undone_ts FROM proposals WHERE id = ?", id).Scan(&undone); err != nil {
		t.Fatal(err)
	}
	if undone == nil {
		t.Fatal("the undone proposal was not marked undone")
	}
}

func TestRecoveredProposalWithRecordedWorkCannotBeApprovedAgain(t *testing.T) {
	const hash = "ffffffffffffffffffffffffffffffffffffffff"
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	mnpMediaRoot(t, dir)
	qb, qbURL := mnpNewQbit(t)
	setEnv(t, settingEnv(media.KeyQbitURL), qbURL)
	id := proposalID(t, toolResult(t, "media_add_magnet", map[string]any{"magnet": mediaMagnet(hash, "Lanterns.S01E11")}))
	if got := runApp(t, "proposals", "approve", id); got.code != cli.ExitOK {
		t.Fatalf("approve: %d %q %q", got.code, got.stdout, got.stderr)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
	defer func() { _ = d.Close() }()
	if _, err := d.Write.Exec("UPDATE proposals SET status = 'failed', recovered = 1 WHERE id = ?", id); err != nil {
		t.Fatal(err)
	}
	got := runApp(t, "proposals", "approve", id)
	if got.code == cli.ExitOK || !strings.Contains(got.stderr, "already has a completed write") {
		t.Fatalf("re-approving a recovered proposal = %d %q %q, want a refusal", got.code, got.stdout, got.stderr)
	}
	if adds := qb.count("add"); adds != 1 {
		t.Fatalf("the refused re-approval reached qBittorrent: %d adds", adds)
	}
	if got := runApp(t, "proposals", "undo", id); got.code != cli.ExitOK {
		t.Fatalf("proposals undo: %d %q %q", got.code, got.stdout, got.stderr)
	}
	if deletes := qb.count("delete"); deletes != 1 {
		t.Fatalf("%d deletes after undoing the recovered proposal, want 1", deletes)
	}
}

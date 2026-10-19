package media

import (
	"context"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/db/dbtest"
)

type world struct {
	root string
	qb   *fakeQbit
	ez   *fakeEztv
	nt   *fakeNtfy
	d    *db.DB
	env  config.Env
	cfg  config.MediaSettings
	q    *Qbit
}

func newWorld(t *testing.T) *world {
	t.Helper()
	root := t.TempDir()
	for _, d := range []string{"import", "movies", "tv", "downloads"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o777); err != nil {
			t.Fatal(err)
		}
	}
	w := &world{root: root, qb: newFakeQbit(t), ez: newFakeEztv(t), nt: newFakeNtfy(t), d: dbtest.Schema(t)}
	w.env = config.Map(map[string]string{
		"ATRIUM_MEDIA_ROOT": root, "QBITTORRENT_URL": w.qb.URL, "ATRIUM_PUBLIC_URL": "https://atrium.test",
		"NTFY_URL": w.nt.URL, "NTFY_TOKEN": "tok", "NTFY_MEDIA_TOPIC": "media-releases",
	})
	w.cfg = config.Media(w.env)
	w.q = NewQbit(w.cfg.QbitURL)
	return w
}

func (w *world) completed(t *testing.T, hash, name string, tags []string, files ...string) string {
	t.Helper()
	if len(files) == 0 {
		files = []string{name + ".mkv", "Sample/sample.mkv", "info.nfo"}
	}
	content := filepath.Join(w.root, "downloads", name)
	for _, f := range files {
		full := filepath.Join(content, f)
		if err := os.MkdirAll(filepath.Dir(full), 0o777); err != nil {
			t.Fatal(err)
		}
		size := 100
		if strings.HasSuffix(f, ".nfo") {
			size = 10
		}
		if err := os.WriteFile(full, make([]byte, size), 0o666); err != nil {
			t.Fatal(err)
		}
	}
	w.qb.add(hash, name, tags, StoppedUP, 1, content)
	return content
}

func (w *world) channel(t *testing.T, name, imdb, pattern string, primed bool) *Channel {
	t.Helper()
	c, err := AddChannel(context.Background(), w.d.Write, name, imdb, pattern, false, nil, func() float64 { return now }, primed)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func (w *world) poll(t *testing.T, at float64, opts PollOptions) PollResult {
	t.Helper()
	opts.Now = func() float64 { return at }
	out, err := Poll(context.Background(), w.d.Write, w.env, w.q, w.cfg, opts)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func hashes(added []Added) []string {
	out := []string{}
	for _, a := range added {
		out = append(out, a.Hash)
	}
	return out
}

func TestMagnetAndTorrentHashes(t *testing.T) {
	if got, ok := MagnetHash("magnet:?xt=urn:btih:" + strings.ToUpper(hashA) + "&dn=x"); !ok || got != hashA {
		t.Errorf("hex magnet = %q %v", got, ok)
	}
	raw, _ := hex.DecodeString(hashB)
	if got, ok := MagnetHash("magnet:?dn=y&xt=urn:btih:" + base32.StdEncoding.EncodeToString(raw)); !ok || got != hashB {
		t.Errorf("base32 magnet = %q %v", got, ok)
	}
	for _, none := range []string{"magnet:?dn=only", "http://x"} {
		if _, ok := MagnetHash(none); ok {
			t.Errorf("%q has no hash", none)
		}
	}
	data, want := torrentBytes()
	if got, err := TorrentHash(data); err != nil || got != want {
		t.Errorf("torrent hash = %q %v, want %q", got, err, want)
	}
	var qbitErr QbitError
	if _, err := TorrentHash([]byte("not a torrent")); !errors.As(err, &qbitErr) {
		t.Errorf("garbage torrent: %v", err)
	}
	if !(Torrent{Tags: "media, filed"}).HasTag("filed") || (Torrent{}).HasTag("media") {
		t.Error("HasTag")
	}
	if !accepted("Ok.") || !accepted(`{"added_torrent_ids":["e2"],"failure_count":0,"pending_count":0,"success_count":1}`) ||
		accepted("Fails.") || accepted(`{"failure_count":1,"pending_count":0,"success_count":0}`) {
		t.Error("accepted")
	}
}

func TestFilingHardlinksTorrentAndTagsFiled(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	content := w.completed(t, hashA, "Lanterns.S01E08.1080p.WEB.h264-ETHEL", []string{"media"}, "Lanterns.S01E08.1080p.WEB.h264-ETHEL.mkv", "Sample/sample.mkv", "info.nfo")
	items, errText, err := Items(ctx, w.q, w.root)
	if err != nil || errText != "" || len(items) != 1 || items[0].ID != hashA {
		t.Fatalf("items = %+v %q %v", items, errText, err)
	}
	view := Defaults(items[0], w.root)
	if view.Kind != KindTV || view.Title != "Lanterns" || view.Year != "" {
		t.Fatalf("defaults = %+v", view)
	}
	ops, err := Plan(&view, w.root)
	src := filepath.Join(content, "Lanterns.S01E08.1080p.WEB.h264-ETHEL.mkv")
	if err != nil || !reflect.DeepEqual(ops, []Op{{src, "tv/Lanterns/Season 01/Lanterns - S01E08.mkv"}}) {
		t.Fatalf("plan = %+v %v", ops, err)
	}
	if err := Apply(ctx, w.q, items[0], ops, w.root); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(w.root, "tv/Lanterns/Season 01/Lanterns - S01E08.mkv")
	if !sameFile(t, src, dst) {
		t.Error("destination is not a hardlink of the source")
	}
	if !slices.Contains(w.qb.tagsOf(hashA), TagFiled) {
		t.Error("filed tag missing")
	}
	if again, _, _ := Items(ctx, w.q, w.root); len(again) != 0 {
		t.Errorf("filed torrent still waits: %+v", again)
	}
	again := Defaults(items[0], w.root)
	var planErr PlanError
	if _, err := Plan(&again, w.root); !errors.As(err, &planErr) || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("replan = %v", err)
	}
}

func sameFile(t *testing.T, a, b string) bool {
	t.Helper()
	ia, err := os.Stat(a)
	if err != nil {
		t.Fatal(err)
	}
	ib, err := os.Stat(b)
	if err != nil {
		t.Fatal(err)
	}
	return os.SameFile(ia, ib)
}

func TestSingleFileTorrentIsFiled(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	name := "VisionQuest.S01E01.1080p.HEVC.x265-MeGusta[EZTVx.to].mkv"
	content := filepath.Join(w.root, "downloads", name)
	if err := os.WriteFile(content, make([]byte, 100), 0o666); err != nil {
		t.Fatal(err)
	}
	w.qb.add(hashA, name, []string{"media"}, StoppedUP, 1, content)
	items, _, err := Items(ctx, w.q, w.root)
	if err != nil || len(items) != 1 {
		t.Fatalf("items = %+v %v", items, err)
	}
	view := Defaults(items[0], w.root)
	ops, err := Plan(&view, w.root)
	if err != nil || !reflect.DeepEqual(ops, []Op{{content, "tv/VisionQuest/Season 01/VisionQuest - S01E01.mkv"}}) {
		t.Fatalf("plan = %+v %v", ops, err)
	}
}

func TestShowYearJoinsExistingFolder(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	if err := os.MkdirAll(filepath.Join(w.root, "tv/Lanterns (2026)"), 0o777); err != nil {
		t.Fatal(err)
	}
	w.completed(t, hashA, "Lanterns.S01E08.1080p.WEB.h264-ETHEL", []string{"media"}, "Lanterns.S01E08.1080p.WEB.h264-ETHEL.mkv")
	items, _, _ := Items(ctx, w.q, w.root)
	view := Defaults(items[0], w.root)
	ops, err := Plan(&view, w.root)
	if view.Year != "2026" || err != nil || ops[0].Dst != "tv/Lanterns (2026)/Season 01/Lanterns (2026) - S01E08.mkv" {
		t.Fatalf("view = %+v ops = %+v err = %v", view, ops, err)
	}
}

func TestFilingMovesImportAndRemovesEmptyDir(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	src := filepath.Join(w.root, "import/Heart Eyes (2025)")
	if err := os.MkdirAll(src, 0o777); err != nil {
		t.Fatal(err)
	}
	for name, size := range map[string]int{"Heart.Eyes.2025.1080p.mkv": 300, "Heart.Eyes.2025.sample.mkv": 10} {
		if err := os.WriteFile(filepath.Join(src, name), make([]byte, size), 0o666); err != nil {
			t.Fatal(err)
		}
	}
	items, _, _ := Items(ctx, w.q, w.root)
	if len(items) != 1 || items[0].ID != "import/Heart Eyes (2025)" || items[0].Torrent {
		t.Fatalf("items = %+v", items)
	}
	view := Defaults(items[0], w.root)
	ops, err := Plan(&view, w.root)
	if err != nil || view.Kind != KindMovie || view.Title != "Heart Eyes" || view.Year != "2025" ||
		view.MovieFile != "Heart.Eyes.2025.1080p.mkv" || ops[0].Dst != "movies/Heart Eyes (2025)/Heart Eyes (2025).mkv" {
		t.Fatalf("view = %+v ops = %+v err = %v", view, ops, err)
	}
	if err := Apply(ctx, w.q, items[0], ops, w.root); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(w.root, "movies/Heart Eyes (2025)/Heart Eyes (2025).mkv"))
	if err != nil || len(data) != 300 {
		t.Errorf("filed movie = %d bytes, %v", len(data), err)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Errorf("import entry still exists: %v", err)
	}
}

func TestEditedFormOverridesDefaults(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	w.completed(t, hashA, "Lanterns.S01E08", []string{"media"}, "ep1.mkv", "ep2.mkv")
	items, _, _ := Items(ctx, w.q, w.root)
	view := Edited(items[0], w.root, map[string]string{"kind": "tv", "title": "Lanterns", "year": "2026", "s0": "1", "e0": "1", "s1": "1", "e1": ""})
	ops, err := Plan(&view, w.root)
	if err != nil || len(ops) != 1 || ops[0].Dst != "tv/Lanterns (2026)/Season 01/Lanterns (2026) - S01E01.mkv" || view.Files[1].Dest != Skipped {
		t.Fatalf("ops = %+v view = %+v err = %v", ops, view, err)
	}
	dup := Edited(items[0], w.root, map[string]string{"kind": "tv", "title": "L", "s0": "1", "e0": "1", "s1": "1", "e1": "1"})
	if _, err := Plan(&dup, w.root); err == nil || !strings.Contains(err.Error(), "two files") {
		t.Errorf("duplicate destination: %v", err)
	}
	bad := Edited(items[0], w.root, map[string]string{"kind": "tv", "title": "L", "s0": "", "e0": "1"})
	if _, err := Plan(&bad, w.root); err == nil || !strings.Contains(err.Error(), "must be numbers") {
		t.Errorf("empty season: %v", err)
	}
}

func TestUntaggedAndFiledTorrentsAreNeverTouched(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	w.completed(t, hashA, "A", nil)
	w.completed(t, hashB, "B", []string{"media", "filed"})
	w.completed(t, hashC, "C", []string{"media", "ignored"})
	w.qb.add("dddddddddddddddddddddddddddddddddddddddd", "plain", nil, StoppedUP, 1, "", fakeFile{0, "x.exe", 1})
	if items, _, _ := Items(ctx, w.q, w.root); len(items) != 0 {
		t.Errorf("items = %+v", items)
	}
	if done, err := SelectFiles(ctx, w.q, nil); err != nil || len(done) != 0 {
		t.Errorf("select = %v %v", done, err)
	}
	all, err := w.q.Torrents(ctx, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if done, err := SelectFiles(ctx, w.q, all); err != nil || len(done) != 0 {
		t.Errorf("select over the full list = %v %v", done, err)
	}
	for _, write := range w.qb.writes {
		for _, untouched := range []string{hashA, "dddddddddddddddddddddddddddddddddddddddd"} {
			if strings.Contains(write, untouched) {
				t.Errorf("qBittorrent was written for an untagged torrent: %s", write)
			}
		}
	}
	var planErr PlanError
	if err := Ignore(ctx, w.q, Item{ID: "import/x", Name: "x"}); !errors.As(err, &planErr) {
		t.Errorf("ignore of an import = %v", err)
	}
}

func TestChannelPollNeverTouchesUntaggedTorrents(t *testing.T) {
	w := newWorld(t)
	w.qb.add(hashB, "plain", nil, "downloading", 0, "", fakeFile{0, "x.exe", 1}, fakeFile{1, "y.mkv", 0})
	w.ez.release("1234567", hashA, "Lanterns S01E01 1080p", "1500000000")
	w.channel(t, "Lanterns", "tt1234567", "1080p", true)
	if out := w.poll(t, now, PollOptions{}); !slices.Equal(hashes(out.Added), []string{hashA}) {
		t.Fatalf("added = %+v", out.Added)
	}
	for _, write := range w.qb.writes {
		if strings.Contains(write, hashB) {
			t.Errorf("qBittorrent was written for an untagged torrent: %s", write)
		}
	}
	if !reflect.DeepEqual(w.qb.priorities(hashB), []int{1, 0}) {
		t.Errorf("untagged priorities changed: %v", w.qb.priorities(hashB))
	}
}

func TestSweepOnlyAfterStoppedUP(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	w.completed(t, hashA, "A", []string{"media", "filed"})
	w.qb.items[hashA].state = "uploading"
	w.completed(t, hashB, "B", []string{"media", "filed"})
	w.completed(t, hashC, "C", []string{"media"})
	out, err := Sweep(ctx, w.q)
	if err != nil || !reflect.DeepEqual(out, SweepResult{Removed: []string{"B"}, Errors: []string{}}) {
		t.Fatalf("sweep = %+v %v", out, err)
	}
	if !slices.Equal(w.qb.deleted, []string{hashB + " true"}) || w.qb.items[hashA] == nil || w.qb.items[hashC] == nil {
		t.Errorf("deleted %v", w.qb.deleted)
	}
	w.qb.fail = 500
	if out, _ := Sweep(ctx, w.q); len(out.Errors) != 1 {
		t.Errorf("sweep while qBittorrent is down = %+v", out)
	}
}

func TestSelectFilesKeepsVideoAndSubtitlesAndStarts(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	w.qb.add(hashA, "pack", []string{"media"}, "stoppedDL", 0, "",
		fakeFile{0, "ep.mkv", 0}, fakeFile{1, "ep.srt", 0}, fakeFile{2, "readme.nfo", 1}, fakeFile{3, "Sample/sample.mkv", 1})
	w.qb.add(hashB, "meta pending", []string{"media"}, "metaDL", 0, "")
	w.qb.add(hashC, "plain", nil, "downloading", 0, "", fakeFile{0, "x.exe", 1})
	done, err := SelectFiles(ctx, w.q, nil)
	if err != nil || !slices.Equal(done, []string{hashA}) {
		t.Fatalf("select = %v %v", done, err)
	}
	if !reflect.DeepEqual(w.qb.priorities(hashA), []int{1, 1, 0, 0}) || !slices.Contains(w.qb.tagsOf(hashA), TagSelected) || !slices.Equal(w.qb.started, []string{hashA}) {
		t.Errorf("priorities %v tags %v started %v", w.qb.priorities(hashA), w.qb.tagsOf(hashA), w.qb.started)
	}
	if slices.Contains(w.qb.tagsOf(hashB), TagSelected) || !reflect.DeepEqual(w.qb.priorities(hashC), []int{1}) {
		t.Error("a torrent without metadata or without the media tag was selected")
	}
	if again, _ := SelectFiles(ctx, w.q, nil); len(again) != 0 {
		t.Errorf("selection is not idempotent: %v", again)
	}
}

func TestChannelValidationAndCaseToggle(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	for _, c := range []struct{ name, imdb, pattern, want string }{
		{"Lanterns", "123", "x", "IMDb"},
		{"Lanterns", "tt1234567", "(", "pattern"},
		{" ", "tt1234567", "x", "name"},
	} {
		var channelErr ChannelError
		_, err := AddChannel(ctx, w.d.Write, c.name, c.imdb, c.pattern, false, nil, nil, false)
		if !errors.As(err, &channelErr) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("AddChannel(%q, %q, %q) = %v", c.name, c.imdb, c.pattern, err)
		}
	}
	row := w.channel(t, "Lanterns", "tt1234567", `^lanterns s\d+e\d+ 1080p`, false)
	if row.IMDB != "1234567" || row.CaseSensitive || row.Primed {
		t.Fatalf("row = %+v", row)
	}
	releases := []Release{{Hash: "1", Title: "Lanterns S01E08 1080p WEB"}, {Hash: "2", Title: "Lanterns S01E08 720p"}}
	for caseSensitive, want := range map[bool][]bool{false: {true, false}, true: {false, false}} {
		rows, err := Preview(releases, row.Pattern, caseSensitive)
		if err != nil || rows[0].Match != want[0] || rows[1].Match != want[1] {
			t.Errorf("preview case_sensitive=%v = %+v %v", caseSensitive, rows, err)
		}
	}
	if gone, err := DeleteChannel(ctx, w.d.Write, row.ID); err != nil || gone.ID != row.ID {
		t.Fatalf("delete = %+v %v", gone, err)
	}
	if _, err := DeleteChannel(ctx, w.d.Write, row.ID); err == nil {
		t.Error("deleting twice must fail")
	}
}

func TestPollPrimesThenAddsTaggedAndNotifies(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	w.ez.release("1234567", hashA, "Lanterns S01E07 1080p WEB h264", "1500000000")
	w.ez.release("1234567", hashB, "Lanterns S01E07 720p", "1500000000")
	w.channel(t, "Lanterns", "tt1234567", `^Lanterns S\d+E\d+ 1080p`, false)

	if first := w.poll(t, now, PollOptions{}); len(first.Added) != 0 || len(first.Errors) != 0 || len(w.qb.added) != 0 {
		t.Fatalf("priming poll = %+v", first)
	}
	if seen, _ := isSeen(ctx, w.d.Write, hashA); !seen {
		t.Error("the primed release is not recorded as seen")
	}
	if seen, _ := isSeen(ctx, w.d.Write, hashB); seen {
		t.Error("a release the pattern excludes was recorded")
	}
	if list, _ := ListChannels(ctx, w.d.Write); !list[0].Primed {
		t.Error("channel not primed")
	}
	if st, _ := ChannelStatus(ctx, w.d.Write); st.LastCheck != float64(now) || len(st.Errors) != 0 {
		t.Errorf("status = %+v", st)
	}

	w.ez.release("1234567", hashC, "Lanterns S01E08 1080p WEB h264", "2500000000")
	second := w.poll(t, now+60, PollOptions{})
	if !slices.Equal(hashes(second.Added), []string{hashC}) || len(second.Errors) != 0 {
		t.Fatalf("second poll = %+v", second)
	}
	if len(w.qb.added) != 1 || w.qb.added[0].hash != hashC || !slices.Equal(w.qb.added[0].tags, []string{"media"}) {
		t.Errorf("qBittorrent adds = %+v", w.qb.added)
	}
	sent := w.nt.sent[len(w.nt.sent)-1]
	if sent["topic"] != "media-releases" || sent["click"] != "https://atrium.test/media/downloads" || !strings.Contains(sent["message"].(string), "Lanterns S01E08 1080p WEB h264 (2.5 GB)") {
		t.Errorf("ntfy = %+v", sent)
	}
	if again := w.poll(t, now+120, PollOptions{}); len(again.Added) != 0 {
		t.Errorf("a seen release was added again: %+v", again)
	}

	if err := expireSeen(ctx, w.d.Write, now+seenTTL+200); err != nil {
		t.Fatal(err)
	}
	if seen, _ := isSeen(ctx, w.d.Write, hashA); seen {
		t.Error("seen hashes did not expire")
	}
	w.qb.fail = 500
	third := w.poll(t, now+180, PollOptions{})
	if len(third.Added) != 0 || len(third.Errors) == 0 || !strings.Contains(third.Errors[0], "Lanterns") {
		t.Fatalf("poll with qBittorrent down = %+v", third)
	}
	if seen, _ := isSeen(ctx, w.d.Write, hashA); seen {
		t.Error("a failed add was recorded as seen")
	}
	if st, _ := ChannelStatus(ctx, w.d.Write); !slices.Equal(st.Errors, third.Errors) {
		t.Errorf("status errors = %v, want %v", st.Errors, third.Errors)
	}
	w.qb.fail = 0
	w.ez.fail = 503
	down := w.poll(t, now+240, PollOptions{})
	if len(down.Errors) != 1 || down.Errors[0] != "Lanterns: EZTV: 503" {
		t.Errorf("EZTV down = %+v", down.Errors)
	}
}

func TestForcedSingleChannelPollBackfillsAndKeepsGlobalState(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	w.ez.release("1234567", hashA, "Lanterns S01E07 1080p WEB h264", "1500000000")
	w.ez.release("7654321", hashB, "Other S01E01 1080p", "1500000000")
	lanterns := w.channel(t, "Lanterns", "tt1234567", `^Lanterns S\d+E\d+ 1080p`, false)
	w.channel(t, "Other", "tt7654321", "1080p", false)
	w.poll(t, now, PollOptions{})
	if len(w.qb.added) != 0 {
		t.Fatalf("priming added %+v", w.qb.added)
	}
	out := w.poll(t, now+60, PollOptions{Only: &lanterns.ID, Force: true})
	if !slices.Equal(hashes(out.Added), []string{hashA}) || len(w.qb.added) != 1 {
		t.Fatalf("forced poll = %+v", out)
	}
	if st, _ := ChannelStatus(ctx, w.d.Write); st.LastCheck != float64(now) {
		t.Errorf("a single-channel poll changed the global state: %+v", st)
	}
}

func TestPollSelectsFilesOfAddedTorrents(t *testing.T) {
	w := newWorld(t)
	w.ez.release("1234567", hashA, "Lanterns S01E01 1080p", "1500000000")
	w.channel(t, "Lanterns", "tt1234567", "1080p", true)
	w.qb.add(hashA, "Lanterns S01E01", []string{"media"}, "downloading", 0, "", fakeFile{0, "e.mkv", 0}, fakeFile{1, "e.txt", 1})
	out := w.poll(t, now, PollOptions{})
	if !slices.Equal(hashes(out.Added), []string{hashA}) || !reflect.DeepEqual(w.qb.priorities(hashA), []int{1, 0}) || !slices.Contains(w.qb.tagsOf(hashA), TagSelected) {
		t.Errorf("added %+v priorities %v tags %v", out.Added, w.qb.priorities(hashA), w.qb.tagsOf(hashA))
	}
}

func TestApplyDirectoryModes(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	previous := syscall.Umask(0)
	t.Cleanup(func() { syscall.Umask(previous) })
	w.completed(t, hashA, "Lanterns.S01E08", []string{"media"}, "Lanterns.S01E08.mkv")
	items, _, _ := Items(ctx, w.q, w.root)
	view := Defaults(items[0], w.root)
	ops, err := Plan(&view, w.root)
	if err != nil {
		t.Fatal(err)
	}
	if err := Apply(ctx, w.q, items[0], ops, w.root); err != nil {
		t.Fatal(err)
	}
	for dir, want := range map[string]os.FileMode{"tv/Lanterns": 0o777, "tv/Lanterns/Season 01": 0o775} {
		info, err := os.Stat(filepath.Join(w.root, dir))
		if err != nil || info.Mode().Perm() != want {
			t.Errorf("%s mode = %v %v, want %v", dir, info.Mode().Perm(), err, want)
		}
	}
}

func TestLibraryListingAndSearch(t *testing.T) {
	w := newWorld(t)
	for rel, data := range map[string]string{
		"movies/Dune (2021)/Dune (2021).mkv":                        "0123456789",
		"tv/Lanterns (2026)/Season 01/Lanterns (2026) - S01E08.mkv": "episode-bytes",
	} {
		full := filepath.Join(w.root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o777); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(data), 0o666); err != nil {
			t.Fatal(err)
		}
	}
	movies := MovieEntries(w.root)
	if len(movies) != 1 || movies[0].Title != "Dune" || movies[0].Year != "2021" {
		t.Errorf("movies = %+v", movies)
	}
	show, err := Show(w.root, "Lanterns (2026)")
	if err != nil || len(show.Seasons) != 1 || *show.Seasons[0].Number != 1 || len(show.Seasons[0].Episodes) != 1 ||
		*show.Seasons[0].Episodes[0].Season != 1 || *show.Seasons[0].Episodes[0].Episode != 8 || show.Seasons[0].Episodes[0].Size != 13 {
		t.Errorf("show = %+v %v", show, err)
	}
	var notFound NotFound
	if _, err := Movie(w.root, "../x"); !errors.As(err, &notFound) {
		t.Errorf("traversal = %v", err)
	}
	if got := Search(w.root, "lanterns s01e08", SearchLimit); len(got) != 1 || got[0].Kind != "episode" {
		t.Errorf("search = %+v", got)
	}
	if got := Search(w.root, "dune", SearchLimit); len(got) != 1 || got[0].Kind != "movie" || !slices.Equal(got[0].Files, []string{"movies/Dune (2021)/Dune (2021).mkv"}) {
		t.Errorf("search = %+v", got)
	}
	if _, ok := SafeRel(w.root, "movies/../../etc/passwd"); ok {
		t.Error("SafeRel accepted a traversal")
	}
	if _, ok := SafeRel(w.root, "movies/Dune (2021)/Dune (2021).mkv"); !ok {
		t.Error("SafeRel rejected a library file")
	}
}

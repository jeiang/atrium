package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/jeiang/atrium/internal/apps/library"
	"github.com/jeiang/atrium/internal/apps/torrents/contract"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
)

type autoFileSite struct {
	t     *testing.T
	s     *apiSite
	token string
	d     *db.DB
}

func newAutoFileSite(t *testing.T, dir string) *autoFileSite {
	t.Helper()
	seedAllFlagsIn(t, dir)
	token := mediaAPIToken(t)
	s := &apiSite{t: t, base: startServeIn(t, dir)}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
	t.Cleanup(func() { _ = d.Close() })
	return &autoFileSite{t: t, s: s, token: token, d: d}
}

func (a *autoFileSite) call(method, path string, body any) apiReply {
	a.t.Helper()
	got := a.s.do(method, "/api/v1"+path, a.token, body)
	expectAPI(a.t, got, http.StatusOK, "")
	return got
}

func (a *autoFileSite) sweep() float64 {
	a.t.Helper()
	got := a.call(http.MethodPost, "/media/sweep", nil)
	filed, _ := got.json["filed"].(float64)
	return filed
}

func (a *autoFileSite) requests() map[string]map[string]any {
	a.t.Helper()
	out := map[string]map[string]any{}
	items, _ := a.call(http.MethodGet, "/media/requests", nil).json["items"].([]any)
	for _, raw := range items {
		item := raw.(map[string]any)
		out[fmt.Sprint(item["hash"])] = item
	}
	return out
}

func (a *autoFileSite) count(query string, args ...any) int {
	a.t.Helper()
	var n int
	if err := a.d.Read.QueryRow(query, args...).Scan(&n); err != nil {
		a.t.Fatal(err)
	}
	return n
}

func (a *autoFileSite) notices() int {
	return a.count("SELECT COUNT(*) FROM notifications WHERE channel = ? AND title LIKE 'In the library:%'", library.NotifyChannel)
}

func (a *autoFileSite) effectKey(id int64) string {
	a.t.Helper()
	var key string
	if err := a.d.Read.QueryRow("SELECT operation_key FROM effects WHERE id = ?", id).Scan(&key); err != nil {
		a.t.Fatal(err)
	}
	return key
}

func autoFileAgent(t *testing.T, dir, qbURL, eztv string) string {
	t.Helper()
	agent := startMediaAgent(t, mediaAgentOpts{main: true, dir: dir})
	root := filepath.Dir(agent.roots["movies"])
	raw, err := os.ReadFile(mediaHost(t, dir, qbURL, eztv, "", ""))
	if err != nil {
		t.Fatal(err)
	}
	prefix := "\n[media]\n" + library.KeyDownloadPrefix + " = " + strconv.Quote(filepath.Join(root, "downloads")) + "\n"
	setEnv(t, "ATRIUM_HOST_CONFIG", writeFile(t, dir, "host-agent.toml", withAgentConnection(string(raw), agent.base, filepath.Join(agent.dir, "token"))+prefix))
	return root
}

func completed(t *testing.T, qb *mnpQbit, root, hash, name string, files ...string) string {
	t.Helper()
	content := filepath.Join(root, "downloads", name)
	for _, f := range files {
		mnpWrite(t, filepath.Join(content, f), []byte(f))
	}
	qb.put(mnpTorrent{hash: hash, name: name, tags: []string{library.TagMedia}, state: contract.StoppedUP, progress: 1, content: content})
	return content
}

func TestLibraryAutoFilesAnApprovedRequestOnceWithUndo(t *testing.T) {
	t.Parallel()
	const (
		requested = "a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1"
		unclear   = "b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2"
		unasked   = "c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3"
	)
	dir := t.TempDir()
	qb, qbURL := mnpNewQbit(t)
	root := autoFileAgent(t, dir, qbURL, "")
	a := newAutoFileSite(t, dir)

	a.call(http.MethodPost, "/media/torrents", map[string]any{"magnet": mediaMagnet(requested, "Arrival")})
	a.call(http.MethodPost, "/media/torrents", map[string]any{"magnet": mediaMagnet(unclear, "Mystery")})
	before := a.requests()
	if len(before) != 2 || before[requested]["state"] != library.RequestRequested || before[requested]["approved_by"] != library.ApprovedByOperator {
		t.Fatalf("requests after two approved adds: %v", before)
	}
	if a.sweep() != 0 || a.notices() != 0 {
		t.Fatal("a sweep filed an incomplete download")
	}

	content := completed(t, qb, root, requested, "Arrival.2016.1080p.WEB", "Arrival.2016.mkv")
	completed(t, qb, root, unclear, "Mystery.Film.1080p.WEB", "Mystery.Film.mkv")
	completed(t, qb, root, unasked, "Heat.1995.1080p.WEB", "Heat.1995.mkv")
	if filed := a.sweep(); filed != 1 {
		t.Fatalf("sweep filed %v items, want 1", filed)
	}
	id := mediaEffectID(t, a.d, library.OpFile, requested, "done")
	if key := a.effectKey(id); !strings.HasPrefix(key, library.AutoFilePrefix) {
		t.Fatalf("automatic filing key %q", key)
	}
	placed := filepath.Join(root, "movies", "Arrival (2016)", "Arrival (2016).mkv")
	if _, err := os.Stat(placed); err != nil {
		t.Fatalf("the approved request was not placed: %v", err)
	}
	if tags, _ := qb.state(requested); !slices.Contains(tags, library.TagFiled) {
		t.Fatalf("tags after automatic filing %v", tags)
	}
	for _, hash := range []string{unclear, unasked} {
		if n := mediaEffectCount(t, a.d, library.OpFile, hash); n != 0 {
			t.Fatalf("%s: %d filing effects, want 0", hash, n)
		}
	}
	if n := a.notices(); n != 1 {
		t.Fatalf("%d media notices after one automatic filing, want 1", n)
	}
	if n := a.count("SELECT COUNT(*) FROM notifications WHERE channel = ? AND title LIKE '%Arrival (2016)%'", library.NotifyChannel); n != 1 {
		t.Fatal("the notice does not name the filed item")
	}
	if n := a.count(`SELECT COUNT(*) FROM operation_audit WHERE operation = 'media.file-item' AND target = ? AND details_json LIKE '%"automatic":true%'`, requested); n != 1 {
		t.Fatalf("%d automatic filing audit rows, want 1", n)
	}
	after := a.requests()
	if r := after[requested]; r["state"] != library.RequestFiled || r["filed_by"] != "job" || number(t, r["effect_id"]) != id {
		t.Fatalf("filed request %v", r)
	}
	if r := after[unclear]; r["state"] != library.RequestWaiting || !strings.Contains(fmt.Sprint(r["reason"]), "year") {
		t.Fatalf("unclear request %v", r)
	}
	if _, ok := after[unasked]; ok {
		t.Fatal("an unrequested download has a request")
	}

	if ran := runApp(t, "effects", "undo", fmt.Sprint(id)); ran.code != cli.ExitOK {
		t.Fatalf("effects undo: %d %s", ran.code, ran.stderr)
	}
	if _, err := os.Stat(placed); !os.IsNotExist(err) {
		t.Fatalf("the undo left the filed link: %v", err)
	}
	if _, err := os.Stat(filepath.Join(content, "Arrival.2016.mkv")); err != nil {
		t.Fatalf("the undo removed the download: %v", err)
	}
	qb.put(mnpTorrent{hash: requested, name: "Arrival.2016.1080p.WEB", tags: []string{library.TagMedia}, state: contract.StoppedUP, progress: 1, content: content})
	if filed := a.sweep(); filed != 0 {
		t.Fatalf("a sweep after undo filed %v items", filed)
	}
	if n := mediaEffectCount(t, a.d, library.OpFile, requested); n != 1 || a.notices() != 1 {
		t.Fatalf("after undo: %d filing effects, %d notices, want 1 and 1", n, a.notices())
	}
}

func TestLibraryAutoFilesAFollowedShowEpisode(t *testing.T) {
	t.Parallel()
	const (
		episode = "d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4"
		stray   = "e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5"
	)
	var mu sync.Mutex
	releases := []map[string]any{}
	eztv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"torrents": releases})
	}))
	t.Cleanup(eztv.Close)
	dir := t.TempDir()
	qb, qbURL := mnpNewQbit(t)
	root := autoFileAgent(t, dir, qbURL, eztv.URL+"/api/get-torrents")
	if err := os.MkdirAll(filepath.Join(root, "tv", "Lanterns (2026)"), 0o755); err != nil {
		t.Fatal(err)
	}
	a := newAutoFileSite(t, dir)

	a.call(http.MethodPost, "/media/channels", map[string]any{"name": "Lanterns", "imdb": "tt1234567", "pattern": "1080p"})
	a.call(http.MethodPost, "/media/channels/poll", nil)
	mu.Lock()
	releases = append(releases, map[string]any{"hash": episode, "title": "Lanterns S01E03 1080p WEB", "magnet_url": mediaMagnet(episode, "Lanterns.S01E03"), "size_bytes": "1000000", "season": "1", "episode": "3"})
	mu.Unlock()
	a.call(http.MethodPost, "/media/channels/poll", nil)
	mediaEffectID(t, a.d, contract.OpAddMagnet, episode, "done")
	if len(a.requests()) != 0 {
		t.Fatal("a channel add was recorded as a request")
	}

	completed(t, qb, root, episode, "Lanterns.S01E03.1080p.WEB", "Lanterns.S01E03.mkv")
	completed(t, qb, root, stray, "Lanterns.S01E04.1080p.WEB", "Lanterns.S01E04.mkv")
	if filed := a.sweep(); filed != 1 {
		t.Fatalf("sweep filed %v items, want 1", filed)
	}
	mediaEffectID(t, a.d, library.OpFile, episode, "done")
	if _, err := os.Stat(filepath.Join(root, "tv", "Lanterns (2026)", "Season 01", "Lanterns (2026) - S01E03.mkv")); err != nil {
		t.Fatalf("the followed episode was not placed: %v", err)
	}
	if n := mediaEffectCount(t, a.d, library.OpFile, stray); n != 0 {
		t.Fatalf("an episode the channel did not add was filed %d times", n)
	}
	if n := a.notices(); n != 1 {
		t.Fatalf("%d media notices, want 1", n)
	}
}

func TestLibraryAutoFileFailureIsNotRetried(t *testing.T) {
	t.Parallel()
	const hash = "f6f6f6f6f6f6f6f6f6f6f6f6f6f6f6f6f6f6f6f6"
	dir := t.TempDir()
	fake := &droppingAgent{}
	srv := httptest.NewServer(http.HandlerFunc(fake.handle))
	t.Cleanup(srv.Close)
	token := writeFile(t, dir, "agent.token", strings.Repeat("t", 40)+"\n")
	qb, qbURL := mnpNewQbit(t)
	root := filepath.Join(dir, "library")
	useMediaAgent(t, dir, qbURL, srv.URL, token, root)
	a := newAutoFileSite(t, dir)

	a.call(http.MethodPost, "/media/torrents", map[string]any{"magnet": mediaMagnet(hash, "Movie")})
	qb.put(mnpTorrent{hash: hash, name: "Movie.2020", tags: []string{library.TagMedia}, state: contract.StoppedUP, progress: 1, content: filepath.Join(root, "downloads", "Movie.2020")})
	if filed := a.sweep(); filed != 0 {
		t.Fatalf("a dropped filing counted as filed: %v", filed)
	}
	id := mediaEffectID(t, a.d, library.OpFile, hash, "unknown")
	if r := a.requests()[hash]; r["state"] != library.RequestFailed || number(t, r["effect_id"]) != id || r["reason"] == nil {
		t.Fatalf("failed request %v", r)
	}
	a.sweep()
	fake.mu.Lock()
	puts := len(fake.puts)
	fake.mu.Unlock()
	if puts != 1 || mediaEffectCount(t, a.d, library.OpFile, hash) != 1 {
		t.Fatalf("filing attempts %d, effects %d, want 1 and 1", puts, mediaEffectCount(t, a.d, library.OpFile, hash))
	}
	if a.notices() != 0 {
		t.Fatal("a failed automatic filing sent a filed notice")
	}
}

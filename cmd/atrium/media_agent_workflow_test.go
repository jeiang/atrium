package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/media"
	"github.com/jeiang/atrium/internal/mediaagent/protocol"
)

func useMediaAgent(t *testing.T, dir, qbURL, agentBase, token, root string) {
	t.Helper()
	raw, err := os.ReadFile(mediaHost(t, dir, qbURL, "", "", ""))
	if err != nil {
		t.Fatal(err)
	}
	prefix := "\n[media]\n" + media.KeyDownloadPrefix + " = " + strconv.Quote(filepath.Join(root, "downloads")) + "\n"
	setEnv(t, "ATRIUM_HOST_CONFIG", writeFile(t, dir, "host-agent.toml", string(raw)+agentConnectionTOML(agentBase, token)+prefix))
	setEnv(t, settingEnv(media.KeyRoot), root)
}

func agentLibrary(t *testing.T, dir, qbURL string) (*mediaAgent, string) {
	t.Helper()
	agent := startMediaAgent(t, mediaAgentOpts{main: true, dir: dir})
	root := filepath.Dir(agent.roots["movies"])
	useMediaAgent(t, dir, qbURL, agent.base, filepath.Join(agent.dir, "token"), root)
	return agent, root
}

func TestFilingThroughTheAgentTwiceHasOneResult(t *testing.T) {
	t.Parallel()
	const hash = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	name := "Lanterns.S01E01.S01E02.1080p.WEB"
	dir := t.TempDir()
	qb, qbURL := mnpNewQbit(t)
	_, root := agentLibrary(t, dir, qbURL)
	content := filepath.Join(root, "downloads", name)
	mnpWrite(t, filepath.Join(content, "Lanterns.S01E01.mkv"), []byte("one"))
	mnpWrite(t, filepath.Join(content, "Lanterns.S01E02.mkv"), []byte("two"))
	qb.put(mnpTorrent{hash: hash, name: name, tags: []string{media.TagMedia}, state: media.StoppedUP, progress: 1, content: content})

	seedAllFlagsIn(t, dir)
	b := mnpNewBrowser(t, startServeIn(t, dir))
	b.want(http.MethodGet, "/apps/media/downloads", nil, http.StatusOK)
	form := url.Values{"id": {hash}, "action": {"file"}, "kind": {"tv"}, "title": {"Lanterns"}, "year": {"2026"},
		"s0": {"1"}, "e0": {"1"}, "s1": {"1"}, "e1": {"2"}}
	b.want(http.MethodPost, "/apps/media/file", form, http.StatusOK)
	if code, _ := b.do(http.MethodPost, "/apps/media/file", form); code == http.StatusOK {
		t.Fatal("the second filing succeeded")
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), db.Key{})
	defer func() { _ = d.Close() }()
	id := mediaEffectID(t, d, media.OpFile, hash, "done")
	if n := mediaEffectCount(t, d, media.OpFile, hash); n != 1 {
		t.Fatalf("filing effects = %d, want 1", n)
	}
	if tags, _ := qb.state(hash); !slices.Contains(tags, media.TagFiled) {
		t.Fatalf("tags after filing %v", tags)
	}
	season := filepath.Join(root, "tv", "Lanterns (2026)", "Season 01")
	for i, ep := range []string{"S01E01", "S01E02"} {
		placed, err := os.Stat(filepath.Join(season, "Lanterns (2026) - "+ep+".mkv"))
		if err != nil {
			t.Fatal(err)
		}
		src, err := os.Stat(filepath.Join(content, fmt.Sprintf("Lanterns.S01E0%d.mkv", i+1)))
		if err != nil || !os.SameFile(placed, src) {
			t.Fatalf("placement %s is not a hardlink to the download: %v", ep, err)
		}
	}
	if ran := runApp(t, "effects", "undo", fmt.Sprint(id)); ran.code != cli.ExitOK {
		t.Fatalf("effects undo: %d %s", ran.code, ran.stderr)
	}
	if _, err := os.Stat(filepath.Join(season, "Lanterns (2026) - S01E01.mkv")); !os.IsNotExist(err) {
		t.Fatalf("the undo left the filed link: %v", err)
	}
	if _, err := os.Stat(filepath.Join(content, "Lanterns.S01E01.mkv")); err != nil {
		t.Fatalf("the undo removed the download: %v", err)
	}
}

type droppingAgent struct {
	mu      sync.Mutex
	puts    []string
	prepare int
}

func (f *droppingAgent) handle(w http.ResponseWriter, r *http.Request) {
	reply := func(v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	id := protocol.Identity{Device: 1, Inode: 2, Size: 5, MTimeNS: 3}
	switch {
	case r.URL.Path == "/v1/source":
		var req protocol.SourceRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		reply(protocol.Source{Files: []protocol.File{{Path: protocol.Path{Root: req.Source.Root, Rel: req.Source.Rel + "/Movie.2020.mkv"}, Identity: id}}})
	case r.URL.Path == "/v1/imports":
		reply(protocol.ImportPage{Entries: []protocol.Path{}})
	case r.URL.Path == "/v1/entries":
		reply(protocol.Entries{Entries: []protocol.Entry{}})
	case r.URL.Path == "/v1/filing/prepare":
		var req protocol.PrepareRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.mu.Lock()
		f.prepare++
		f.mu.Unlock()
		plan := protocol.FilingPlan{Version: 1, AgentID: "fake-agent", RootRevision: "r1", SourceKind: req.SourceKind, TorrentHash: req.TorrentHash}
		for i, p := range req.Placements {
			plan.Steps = append(plan.Steps, protocol.Step{Index: i, Kind: protocol.StepLink, Source: &protocol.File{Path: p.Source, Identity: id}, Destination: p.Destination})
		}
		reply(protocol.PrepareResponse{Plan: plan, PlanDigest: protocol.CanonicalDigest(plan)})
	case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/v1/filings/"):
		f.mu.Lock()
		f.puts = append(f.puts, strings.TrimPrefix(r.URL.Path, "/v1/filings/"))
		f.mu.Unlock()
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			_ = conn.Close()
		}
	case strings.HasSuffix(r.URL.Path, "/inspect"):
		key := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/filings/"), "/inspect")
		reply(protocol.Inspection{Result: protocol.FilingResult{Key: key, State: "unknown", Steps: []protocol.StepResult{}}, Evidence: []protocol.Evidence{{Index: 0, Source: &id}}})
	default:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(protocol.Problem{Code: protocol.CodeNotFound, Mutation: protocol.MutationNone})
	}
}

func TestFilingUnknownBlocksAndIsInspectable(t *testing.T) {
	t.Parallel()
	const hash = "abababababababababababababababababababab"
	dir := t.TempDir()
	fake := &droppingAgent{}
	srv := httptest.NewServer(http.HandlerFunc(fake.handle))
	t.Cleanup(srv.Close)
	token := writeFile(t, dir, "agent.token", strings.Repeat("t", 40)+"\n")
	qb, qbURL := mnpNewQbit(t)
	root := filepath.Join(dir, "library")
	useMediaAgent(t, dir, qbURL, srv.URL, token, root)
	qb.put(mnpTorrent{hash: hash, name: "Movie.2020", tags: []string{media.TagMedia}, state: media.StoppedUP, progress: 1, content: filepath.Join(root, "downloads", "Movie.2020")})

	seedAllFlagsIn(t, dir)
	b := mnpNewBrowser(t, startServeIn(t, dir))
	b.want(http.MethodGet, "/apps/media/downloads", nil, http.StatusOK)
	form := url.Values{"id": {hash}, "action": {"file"}, "kind": {"movie"}, "title": {"Movie"}, "year": {"2020"}}
	if code, _ := b.do(http.MethodPost, "/apps/media/file", form); code == http.StatusOK {
		t.Fatal("a dropped filing reported success")
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), db.Key{})
	defer func() { _ = d.Close() }()
	id := mediaEffectID(t, d, media.OpFile, hash, "unknown")
	if code, _ := b.do(http.MethodPost, "/apps/media/file", form); code == http.StatusOK {
		t.Fatal("the repeat of an unknown filing succeeded")
	}
	fake.mu.Lock()
	puts := slices.Clone(fake.puts)
	fake.mu.Unlock()
	if len(puts) != 1 || mediaEffectCount(t, d, media.OpFile, hash) != 1 {
		t.Fatalf("filing attempts %v, effects %d", puts, mediaEffectCount(t, d, media.OpFile, hash))
	}
	if tags, _ := qb.state(hash); slices.Contains(tags, media.TagFiled) {
		t.Fatalf("an unknown filing tagged the torrent %v", tags)
	}
	ran := runApp(t, "media", "inspect-filing", fmt.Sprint(id))
	if ran.code != cli.ExitOK {
		t.Fatalf("inspect-filing: %d %s", ran.code, ran.stderr)
	}
	var shown struct {
		EffectID int64               `json:"effect_id"`
		State    string              `json:"state"`
		Key      string              `json:"key"`
		Receipt  protocol.Inspection `json:"receipt"`
	}
	if err := json.Unmarshal([]byte(ran.stdout), &shown); err != nil {
		t.Fatal(err)
	}
	if shown.EffectID != id || shown.State != "unknown" || shown.Key != puts[0] || shown.Receipt.Result.Key != puts[0] || len(shown.Receipt.Evidence) != 1 {
		t.Fatalf("inspection %+v, put %v", shown, puts)
	}
}

func TestFilingRefusesUntaggedAndOutsidePrefix(t *testing.T) {
	t.Parallel()
	const outside, plain = "cdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcd", "efefefefefefefefefefefefefefefefefefefef"
	dir := t.TempDir()
	qb, qbURL := mnpNewQbit(t)
	_, root := agentLibrary(t, dir, qbURL)
	elsewhere := filepath.Join(dir, "elsewhere", "Movie.2020")
	mnpWrite(t, filepath.Join(elsewhere, "Movie.2020.mkv"), []byte("outside"))
	inside := filepath.Join(root, "downloads", "Plain.2021")
	mnpWrite(t, filepath.Join(inside, "Plain.2021.mkv"), []byte("plain"))
	qb.put(mnpTorrent{hash: outside, name: "Movie.2020", tags: []string{media.TagMedia}, state: media.StoppedUP, progress: 1, content: elsewhere})
	qb.put(mnpTorrent{hash: plain, name: "Plain.2021", state: media.StoppedUP, progress: 1, content: inside})

	seedAllFlagsIn(t, dir)
	b := mnpNewBrowser(t, startServeIn(t, dir))
	b.want(http.MethodGet, "/apps/media/downloads", nil, http.StatusOK)
	b.want(http.MethodPost, "/apps/media/file", url.Values{"id": {outside}, "action": {"file"}, "kind": {"movie"}, "title": {"Movie"}, "year": {"2020"}}, http.StatusUnprocessableEntity)
	if code, _ := b.do(http.MethodPost, "/apps/media/file", url.Values{"id": {plain}, "action": {"file"}, "kind": {"movie"}, "title": {"Plain"}, "year": {"2021"}}); code == http.StatusOK {
		t.Fatal("an untagged torrent was filed")
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), db.Key{})
	defer func() { _ = d.Close() }()
	if mediaEffectCount(t, d, media.OpFile, outside)+mediaEffectCount(t, d, media.OpFile, plain) != 0 {
		t.Fatal("a refused filing recorded an effect")
	}
	if entries, err := os.ReadDir(filepath.Join(root, "movies")); err != nil || len(entries) != 0 {
		t.Fatalf("the library changed: %v %v", entries, err)
	}
	if qb.touched(outside) || qb.touched(plain) {
		t.Fatalf("a refused filing wrote to qBittorrent: %v", qb.writes)
	}
}

func TestImportFilingRenamesAndCleansUp(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	_, qbURL := mnpNewQbit(t)
	_, root := agentLibrary(t, dir, qbURL)
	movie := filepath.Join(root, "import", "Movie.2020")
	mnpWrite(t, filepath.Join(movie, "Movie.2020.mkv"), []byte("movie bytes"))
	mnpWrite(t, filepath.Join(movie, "Movie.2020.nfo"), []byte("notes"))
	other := filepath.Join(root, "import", "Other.2021")
	mnpWrite(t, filepath.Join(other, "Other.2021.mkv"), []byte("other bytes"))
	mnpWrite(t, filepath.Join(other, "Other.2021.sample.mkv"), []byte("sample"))

	seedAllFlagsIn(t, dir)
	b := mnpNewBrowser(t, startServeIn(t, dir))
	b.want(http.MethodGet, "/apps/media/downloads", nil, http.StatusOK)
	b.want(http.MethodPost, "/apps/media/file", url.Values{"id": {media.Import + "/Movie.2020"}, "action": {"file"}, "kind": {"movie"}, "title": {"Movie"}, "year": {"2020"}}, http.StatusOK)
	b.want(http.MethodPost, "/apps/media/file", url.Values{"id": {media.Import + "/Other.2021"}, "action": {"file"}, "kind": {"movie"}, "title": {"Other"}, "year": {"2021"}}, http.StatusOK)
	placed := filepath.Join(root, "movies", "Movie (2020)", "Movie (2020).mkv")
	if body, err := os.ReadFile(placed); err != nil || string(body) != "movie bytes" {
		t.Fatalf("filed movie %q %v", body, err)
	}
	if _, err := os.Stat(movie); !os.IsNotExist(err) {
		t.Fatalf("the import residue remains: %v", err)
	}
	if _, err := os.Stat(filepath.Join(other, "Other.2021.sample.mkv")); err != nil {
		t.Fatalf("the cleanup removed an import that still holds a video: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "movies", "Other (2021)", "Other (2021).mkv")); err != nil {
		t.Fatal(err)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), db.Key{})
	defer func() { _ = d.Close() }()
	mediaEffectID(t, d, media.OpImportCleanup, media.Import+"/Movie.2020", "done")
	if n := mediaEffectCount(t, d, media.OpImportCleanup, media.Import+"/Other.2021"); n != 0 {
		t.Fatalf("cleanup effects for an import with a sample = %d", n)
	}
	id := mediaEffectID(t, d, media.OpFile, media.Import+"/Other.2021", "done")
	changed := filepath.Join(root, "movies", "Other (2021)", "Other (2021).mkv")
	if err := os.WriteFile(changed+".new", []byte("replaced by hand"), 0o664); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(changed+".new", changed); err != nil {
		t.Fatal(err)
	}
	ran := runApp(t, "effects", "undo", fmt.Sprint(id))
	var undone struct {
		State string `json:"state"`
	}
	if ran.code == cli.ExitOK && (json.Unmarshal([]byte(ran.stdout), &undone) != nil || undone.State == string(ledger.Done)) {
		t.Fatalf("the undo of a changed destination succeeded: %s", ran.stdout)
	}
	if body, err := os.ReadFile(changed); err != nil || string(body) != "replaced by hand" {
		t.Fatalf("the refused undo touched the destination: %q %v", body, err)
	}
	if _, err := os.Stat(filepath.Join(other, "Other.2021.mkv")); !os.IsNotExist(err) {
		t.Fatalf("the refused undo restored the source: %v", err)
	}
}

func TestLegacyFilingUndoIsRefused(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	_, qbURL := mnpNewQbit(t)
	_, root := agentLibrary(t, dir, qbURL)
	dst := filepath.Join(root, "movies", "Old (2001)", "Old (2001).mkv")
	mnpWrite(t, dst, []byte("old"))
	seedAllFlagsIn(t, dir)
	startServeIn(t, dir)
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), db.Key{})
	defer func() { _ = d.Close() }()
	store := ledger.NewStore(d, nil)
	ctx := context.Background()
	ticket, err := store.Begin(ctx, ledger.New{Origin: "media", Operation: media.OpFile, OperationKey: "media:file:legacy:1", Target: "import/Old.2001", Action: json.RawMessage(`{"item":"import/Old.2001"}`)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Settle(ctx, ticket, ledger.Result{State: ledger.Done}, nil); err != nil {
		t.Fatal(err)
	}
	undo, _ := json.Marshal(map[string]any{"op": media.UndoFilePlace, "step": map[string]any{"src": filepath.Join(root, "import", "Old.2001", "Old.2001.mkv"), "dst": dst, "rel": "movies/Old (2001)/Old (2001).mkv"}})
	if _, err := d.Write.Exec("UPDATE effects SET undo_json = ? WHERE id = ?", string(undo), ticket.ID); err != nil {
		t.Fatal(err)
	}
	if ran := runApp(t, "effects", "undo", fmt.Sprint(ticket.ID)); ran.code == cli.ExitOK {
		t.Fatal("a legacy filing undo ran")
	}
	if body, err := os.ReadFile(dst); err != nil || string(body) != "old" {
		t.Fatalf("the refused legacy undo touched the file: %q %v", body, err)
	}
}

package pages

import (
	"encoding/base64"
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

	"github.com/jeiang/atrium/internal/media"
)

const (
	dlHashA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	dlHashB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	dlHashC = "cccccccccccccccccccccccccccccccccccccccc"
)

type dlFile struct {
	Index    int     `json:"index"`
	Name     string  `json:"name"`
	Priority int     `json:"priority"`
	Size     int64   `json:"size"`
	Progress float64 `json:"progress"`
}

type dlTorrent struct {
	hash, name, state, contentPath string
	tags                           []string
	progress                       float64
	size, dlspeed, eta             int64
	files                          []dlFile
}

type dlAddRecord struct {
	kind, magnet, name, hash string
	tags                     []string
}

type dlQbitFake struct {
	mu      sync.Mutex
	order   []string
	items   map[string]*dlTorrent
	added   []dlAddRecord
	started []string
	fail    int
	URL     string
}

func newDlQbit(t *testing.T) *dlQbitFake {
	t.Helper()
	f := &dlQbitFake{items: map[string]*dlTorrent{}}
	srv := httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(srv.Close)
	f.URL = srv.URL
	return f
}

func (f *dlQbitFake) add(tor dlTorrent) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if tor.contentPath == "" {
		tor.contentPath = tor.name
	}
	if _, ok := f.items[tor.hash]; !ok {
		f.order = append(f.order, tor.hash)
	}
	f.items[tor.hash] = &tor
}

func (f *dlQbitFake) tagsOf(hash string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.items[hash].tags)
}

func (f *dlQbitFake) prioritiesOf(hash string) []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []int
	for _, file := range f.items[hash].files {
		out = append(out, file.Priority)
	}
	return out
}

func (f *dlQbitFake) setFail(code int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail = code
}

func dlSplitTags(s string) []string {
	var out []string
	for _, tag := range strings.Split(s, ",") {
		if tag = strings.TrimSpace(tag); tag != "" {
			out = append(out, tag)
		}
	}
	return out
}

func (f *dlQbitFake) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != 0 {
		http.Error(w, "down", f.fail)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/v2/")
	q := r.URL.Query()
	switch path {
	case "torrents/info":
		rows := []map[string]any{}
		for _, h := range f.order {
			t := f.items[h]
			if q.Get("filter") == "completed" && t.progress < 1 {
				continue
			}
			if q.Get("tag") != "" && !slices.Contains(t.tags, q.Get("tag")) {
				continue
			}
			if q.Get("hashes") != "" && !slices.Contains(strings.Split(q.Get("hashes"), "|"), h) {
				continue
			}
			rows = append(rows, map[string]any{
				"hash": t.hash, "name": t.name, "tags": strings.Join(t.tags, ", "), "state": t.state,
				"content_path": t.contentPath, "progress": t.progress, "size": t.size, "dlspeed": t.dlspeed,
				"upspeed": 0, "eta": t.eta,
			})
		}
		_ = json.NewEncoder(w).Encode(rows)
		return
	case "torrents/files":
		t, ok := f.items[q.Get("hash")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(t.files)
		return
	}
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
		_ = r.ParseMultipartForm(1 << 20)
	} else {
		_ = r.ParseForm()
	}
	form := r.Form
	switch path {
	case "torrents/add":
		tags := dlSplitTags(form.Get("tags"))
		if r.MultipartForm != nil && len(r.MultipartForm.File["torrents"]) > 0 {
			header := r.MultipartForm.File["torrents"][0]
			file, _ := header.Open()
			data := make([]byte, header.Size)
			_, _ = file.Read(data)
			_ = file.Close()
			hash, err := media.TorrentHash(data)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			f.added = append(f.added, dlAddRecord{kind: "file", name: header.Filename, hash: hash, tags: tags})
			f.order = append(f.order, hash)
			f.items[hash] = &dlTorrent{hash: hash, name: header.Filename, state: "downloading", tags: tags, files: []dlFile{{Index: 0, Name: "Dune.2021.mkv", Priority: 1}}}
		} else {
			magnet := form.Get("urls")
			hash, _ := media.MagnetHash(magnet)
			f.added = append(f.added, dlAddRecord{kind: "magnet", magnet: magnet, hash: hash, tags: tags})
			if _, ok := f.items[hash]; !ok && hash != "" {
				f.order = append(f.order, hash)
				f.items[hash] = &dlTorrent{hash: hash, name: "magnet " + hash[:8], state: "metaDL", tags: tags}
			}
		}
		_, _ = fmt.Fprint(w, `{"added_torrent_ids":["x"],"failure_count":0,"pending_count":0,"success_count":1}`)
	case "torrents/addTags":
		for _, h := range strings.Split(form.Get("hashes"), "|") {
			if t, ok := f.items[h]; ok {
				for _, tag := range dlSplitTags(form.Get("tags")) {
					if !slices.Contains(t.tags, tag) {
						t.tags = append(t.tags, tag)
					}
				}
			}
		}
	case "torrents/filePrio":
		t := f.items[form.Get("hash")]
		priority, _ := strconv.Atoi(form.Get("priority"))
		for _, id := range strings.Split(form.Get("id"), "|") {
			i, _ := strconv.Atoi(id)
			t.files[i].Priority = priority
		}
	case "torrents/start":
		for _, h := range strings.Split(form.Get("hashes"), "|") {
			f.started = append(f.started, h)
			if t, ok := f.items[h]; ok {
				t.state = "downloading"
			}
		}
	default:
		http.NotFound(w, r)
	}
}

func dlTorrentBytes(name string) ([]byte, string) {
	info := "d4:name" + fmt.Sprintf("%d:%s", len(name), name) + "12:piece lengthi16384e6:pieces20:" + strings.Repeat("\x00", 20) + "e"
	data := []byte("d8:announce9:http://x/4:info" + info + "e")
	hash, err := media.TorrentHash(data)
	if err != nil {
		panic(err)
	}
	return data, hash
}

func dlWorld(t *testing.T, extra map[string]string) (*site, *dlQbitFake, string) {
	t.Helper()
	qb := newDlQbit(t)
	root := filepath.Join(t.TempDir(), "media")
	for _, d := range []string{"import", "movies", "tv", "downloads"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	vars := map[string]string{"ATRIUM_MEDIA_MEDIA_ROOT": root, "ATRIUM_MEDIA_MEDIA_QBITTORRENT_URL": qb.URL}
	for k, v := range extra {
		vars[k] = v
	}
	return newSite(t, vars, Options{}), qb, root
}

func dlCompletedMedia(t *testing.T, root string, qb *dlQbitFake, hash, name string) string {
	t.Helper()
	content := filepath.Join(root, "downloads", name)
	for _, f := range []string{name + ".mkv", "Sample/sample.mkv", "info.nfo"} {
		full := filepath.Join(content, f)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		size := 100
		if strings.HasSuffix(f, ".nfo") {
			size = 10
		}
		if err := os.WriteFile(full, make([]byte, size), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	qb.add(dlTorrent{hash: hash, name: name, tags: []string{"media"}, state: media.StoppedUP, progress: 1, contentPath: content})
	return content
}

func TestDlAddPagePrefillAndSubmit(t *testing.T) {
	s, qb, _ := dlWorld(t, nil)
	magnet := "magnet:?xt=urn:btih:" + dlHashA + "&dn=Lanterns.S01E08"
	page := status(t, s.get("/media/add?magnet="+url.QueryEscape(magnet)), 200, "add page")
	contains(t, page, `value="`+strings.ReplaceAll(magnet, "&", "&amp;")+`"`, `name="torrent"`, `value="media" checked`)

	done := status(t, s.post("/media/add", url.Values{"magnet": {magnet}, "mode": {"media"}}), 200, "add magnet")
	contains(t, done, "Added the magnet link")
	if got := qb.added[len(qb.added)-1]; got.kind != "magnet" || got.magnet != magnet || got.hash != dlHashA || !slices.Equal(got.tags, []string{"media"}) {
		t.Fatalf("magnet add recorded %+v", got)
	}

	plain := "magnet:?xt=urn:btih:" + dlHashB
	status(t, s.post("/media/add", url.Values{"magnet": {plain}, "mode": {"plain"}}), 200, "add plain")
	if got := qb.added[len(qb.added)-1]; len(got.tags) != 0 {
		t.Fatalf("plain add carries tags %v", got.tags)
	}

	data, hash := dlTorrentBytes("Dune.2021")
	b64 := base64.RawURLEncoding.EncodeToString(data)
	status(t, s.post("/media/add", url.Values{"torrent": {b64}, "name": {"Dune.2021.torrent"}, "mode": {"media"}}), 200, "add file")
	if got := qb.added[len(qb.added)-1]; got.kind != "file" || got.name != "Dune.2021.torrent" || got.hash != hash || !slices.Equal(got.tags, []string{"media"}) {
		t.Fatalf("file add recorded %+v", got)
	}
	if !slices.Contains(qb.tagsOf(hash), "selected") {
		t.Fatalf("selection not applied after add: %v", qb.tagsOf(hash))
	}

	empty := status(t, s.post("/media/add", url.Values{"magnet": {""}, "mode": {"media"}}), 422, "empty add")
	contains(t, empty, "Paste a magnet link or choose a .torrent file.")
	notMagnet := status(t, s.post("/media/add", url.Values{"magnet": {"http://x"}, "mode": {"media"}, "name": {"Kept <x>.torrent"}}), 422, "not a magnet")
	contains(t, notMagnet, "That is not a magnet link.", `value="http://x"`, "Kept &lt;x&gt;.torrent (from the shim)")
	status(t, s.post("/media/add", url.Values{"torrent": {"AAAA"}, "mode": {"media"}}), 422, "not a torrent")

	qb.setFail(500)
	down := status(t, s.post("/media/add", url.Values{"magnet": {magnet}, "mode": {"media"}}), 502, "qbit down")
	contains(t, down, "qBittorrent: torrents/add: 500")
}

func TestDlAddPageKeepsTheGlobalMode(t *testing.T) {
	s, _, _ := dlWorld(t, map[string]string{"ATRIUM_MODE": "live"})
	for _, body := range []string{
		status(t, s.get("/media/add"), 200, "add page"),
		status(t, s.post("/media/add", url.Values{"magnet": {"http://x"}, "mode": {"plain"}}), 422, "add post"),
	} {
		contains(t, body, `action="/live/pause"`)
		lacks(t, body, "Shadow")
	}
	contains(t, status(t, s.post("/media/add", url.Values{"magnet": {"http://x"}, "mode": {"plain"}}), 422, "plain"), `value="plain" checked`)
}

func TestDlDownloadsPageListsTorrentsQueueAndFiles(t *testing.T) {
	s, qb, root := dlWorld(t, nil)
	dlCompletedMedia(t, root, qb, dlHashA, "Lanterns.S01E08.1080p.WEB.h264-ETHEL")
	qb.add(dlTorrent{
		hash: dlHashB, name: "Dune.2021.mkv", tags: []string{"media"}, state: "downloading", progress: 0.4, size: 2_000_000_000,
		dlspeed: 5_000_000, eta: 400,
		files: []dlFile{{Index: 0, Name: "Dune.2021.mkv", Priority: 1, Size: 2_000_000_000, Progress: 0.4}, {Index: 1, Name: "x.nfo", Priority: 0}},
	})

	page := status(t, s.get("/media/downloads"), 200, "downloads")
	contains(t, page, "Lanterns.S01E08.1080p.WEB.h264-ETHEL", "Ready to file (1)", "tv/Lanterns/Season 01/Lanterns - S01E08.mkv",
		"40%", "4.8 MB/s", "ETA 6m", `hx-get="/media/torrents"`, "1.9 GB")

	home := s.get("/media")
	status(t, home, 303, "media home")
	if got := home.Header().Get("Location"); got != "/media/downloads" {
		t.Fatalf("location %q", got)
	}

	partial := status(t, s.get("/media/torrents"), 200, "torrents partial")
	contains(t, partial, "Dune.2021.mkv")
	lacks(t, partial, "<html")

	files := status(t, s.get("/media/torrents/"+dlHashB+"/files"), 200, "files")
	contains(t, files, "Dune.2021.mkv", "Skipped", "Wanted", "1.9 GB · 40%")

	toggled := status(t, s.post("/media/torrents/"+dlHashB+"/files", url.Values{"index": {"1"}, "wanted": {"1"}}), 200, "toggle")
	contains(t, toggled, "Selection changed")
	if got := qb.prioritiesOf(dlHashB); !slices.Equal(got, []int{1, 1}) {
		t.Fatalf("priorities %v", got)
	}
	skipped := status(t, s.post("/media/torrents/"+dlHashB+"/files", url.Values{"index": {"1"}, "wanted": {"0"}}), 200, "toggle off")
	contains(t, skipped, "Selection changed")
	if got := qb.prioritiesOf(dlHashB); !slices.Equal(got, []int{1, 0}) {
		t.Fatalf("priorities %v", got)
	}
	missing := s.post("/media/torrents/"+dlHashB+"/files", url.Values{"wanted": {"1"}})
	body := status(t, missing, 422, "toggle without index")
	contains(t, body, `"missing"`, `"index"`)

	status(t, s.get("/media/torrents/"+dlHashC+"/files"), 404, "unknown torrent")

	qb.setFail(500)
	down := status(t, s.get("/media/downloads"), 200, "downloads qbit down")
	contains(t, down, "qBittorrent: torrents/info: 500")
	lacks(t, down, `hx-get="/media/torrents"`)
	fragment := status(t, s.get("/media/torrents/"+dlHashB+"/files"), 502, "files qbit down")
	contains(t, fragment, "qBittorrent: torrents/info: 500")
	toggleDown := status(t, s.post("/media/torrents/"+dlHashB+"/files", url.Values{"index": {"0"}}), 502, "toggle qbit down")
	contains(t, toggleDown, "qBittorrent: torrents/filePrio: 500")
}

func TestDlDownloadsPageWithoutQbittorrent(t *testing.T) {
	s, _, _ := dlWorld(t, map[string]string{"ATRIUM_MEDIA_MEDIA_QBITTORRENT_URL": ""})
	page := status(t, s.get("/media/downloads"), 200, "downloads")
	contains(t, page, "media_qbittorrent_url is not set", "Ready to file (0)")
	lacks(t, page, "Nothing is waiting", "No torrents")
	status(t, s.get("/media/torrents"), 200, "torrents partial")
	status(t, s.get("/media/torrents/"+dlHashA+"/files"), 502, "files")
	status(t, s.post("/media/add", url.Values{"magnet": {"magnet:?xt=urn:btih:" + dlHashA}, "mode": {"media"}}), 502, "add")
	filed := status(t, s.post("/media/file", url.Values{"id": {dlHashA}, "action": {"file"}}), 422, "file")
	contains(t, filed, "Not filed")
	ignored := status(t, s.post("/media/ignore", url.Values{"id": {dlHashA}}), 422, "ignore")
	contains(t, ignored, "Not ignored")
}

func TestDlFilingFromTheWeb(t *testing.T) {
	s, qb, root := dlWorld(t, nil)
	content := dlCompletedMedia(t, root, qb, dlHashA, "Lanterns.S01E08.1080p.WEB.h264-ETHEL")
	form := func(action, year string) url.Values {
		return url.Values{"id": {dlHashA}, "action": {action}, "kind": {"tv"}, "title": {"Lanterns"}, "year": {year}, "s0": {"1"}, "e0": {"8"}}
	}

	preview := status(t, s.post("/media/file", form("preview", "2026")), 200, "preview")
	contains(t, preview, "tv/Lanterns (2026)/Season 01/Lanterns (2026) - S01E08.mkv")
	if _, err := os.Stat(filepath.Join(root, "tv", "Lanterns (2026)")); !os.IsNotExist(err) {
		t.Fatalf("preview touched the filesystem: %v", err)
	}
	if slices.Contains(qb.tagsOf(dlHashA), "filed") {
		t.Fatal("preview tagged the torrent")
	}

	bad := status(t, s.post("/media/file", form("file", "20x6")), 422, "bad year")
	contains(t, bad, "is not a year")
	if _, err := os.Stat(filepath.Join(root, "tv", "Lanterns (2026)")); !os.IsNotExist(err) {
		t.Fatalf("a rejected plan touched the filesystem: %v", err)
	}

	filed := status(t, s.post("/media/file", form("file", "2026")), 200, "file")
	contains(t, filed, "Filed Lanterns.S01E08")
	src, err := os.Stat(filepath.Join(content, "Lanterns.S01E08.1080p.WEB.h264-ETHEL.mkv"))
	if err != nil {
		t.Fatal(err)
	}
	dst, err := os.Stat(filepath.Join(root, "tv", "Lanterns (2026)", "Season 01", "Lanterns (2026) - S01E08.mkv"))
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(src, dst) {
		t.Fatal("filing did not hardlink the source")
	}
	if !slices.Contains(qb.tagsOf(dlHashA), "filed") {
		t.Fatalf("tags %v", qb.tagsOf(dlHashA))
	}

	status(t, s.post("/media/file", url.Values{"id": {dlHashA}, "action": {"file"}}), 422, "file twice")

	dlCompletedMedia(t, root, qb, dlHashB, "Other.S01E01")
	ignored := status(t, s.post("/media/ignore", url.Values{"id": {dlHashB}}), 200, "ignore")
	contains(t, ignored, "Ignored")
	if !slices.Contains(qb.tagsOf(dlHashB), "ignored") {
		t.Fatalf("tags %v", qb.tagsOf(dlHashB))
	}
	status(t, s.post("/media/ignore", url.Values{"id": {"nope"}}), 422, "ignore unknown")
	missing := s.post("/media/ignore", nil)
	status(t, missing, 422, "ignore without id")
}

func TestDlImportEntryIsMovedNotIgnored(t *testing.T) {
	s, _, root := dlWorld(t, nil)
	dir := filepath.Join(root, "import", "Dune.2021.1080p")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Dune.2021.1080p.mkv"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	page := status(t, s.get("/media/downloads"), 200, "downloads")
	contains(t, page, "import: moved", "Ready to file (1)")
	lacks(t, page, `formaction="/media/ignore"`)
	refused := status(t, s.post("/media/ignore", url.Values{"id": {"import/Dune.2021.1080p"}}), 422, "ignore import")
	contains(t, refused, "Only torrents can be ignored")
	status(t, s.post("/media/file", url.Values{"id": {"import/Dune.2021.1080p"}, "action": {"file"}, "kind": {"movie"}, "title": {"Dune"}, "year": {"2021"}}), 200, "file import")
	if _, err := os.Stat(filepath.Join(root, "movies", "Dune (2021)", "Dune (2021).mkv")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("the emptied import folder is still there: %v", err)
	}
}

func TestDlSelectFilesRunsOnGetAndPoll(t *testing.T) {
	s, qb, _ := dlWorld(t, nil)
	qb.add(dlTorrent{
		hash: dlHashA, name: "Show.S01E01", tags: []string{"media"}, state: "stoppedDL",
		files: []dlFile{{Index: 0, Name: "a.mkv", Priority: 0}, {Index: 1, Name: "a.nfo", Priority: 1}, {Index: 2, Name: "a.srt", Priority: 0}},
	})
	qb.add(dlTorrent{
		hash: dlHashB, name: "Show.S01E02", tags: []string{"media"}, state: "downloading",
		files: []dlFile{{Index: 0, Name: "b.mkv", Priority: 0}, {Index: 1, Name: "b.txt", Priority: 1}},
	})
	qb.add(dlTorrent{
		hash: dlHashC, name: "plain.thing", tags: nil, state: "stoppedDL",
		files: []dlFile{{Index: 0, Name: "c.mkv", Priority: 0}, {Index: 1, Name: "c.txt", Priority: 1}},
	})

	status(t, s.get("/media/downloads"), 200, "downloads")
	if got := qb.prioritiesOf(dlHashA); !slices.Equal(got, []int{1, 0, 1}) {
		t.Fatalf("priorities after the page GET %v", got)
	}
	if !slices.Contains(qb.tagsOf(dlHashA), "selected") || !slices.Contains(qb.started, dlHashA) {
		t.Fatalf("tags %v started %v", qb.tagsOf(dlHashA), qb.started)
	}
	if got := qb.prioritiesOf(dlHashB); !slices.Equal(got, []int{1, 0}) {
		t.Fatalf("priorities after the page GET %v", got)
	}

	qb.add(dlTorrent{
		hash: "dddddddddddddddddddddddddddddddddddddddd", name: "Show.S01E03", tags: []string{"media"}, state: "downloading",
		files: []dlFile{{Index: 0, Name: "d.mkv", Priority: 0}, {Index: 1, Name: "d.txt", Priority: 1}},
	})
	status(t, s.get("/media/torrents"), 200, "poll")
	if got := qb.prioritiesOf("dddddddddddddddddddddddddddddddddddddddd"); !slices.Equal(got, []int{1, 0}) {
		t.Fatalf("priorities after the poll %v", got)
	}

	if got := qb.prioritiesOf(dlHashC); !slices.Equal(got, []int{0, 1}) {
		t.Fatalf("an untagged torrent was changed: %v", got)
	}
	if len(qb.tagsOf(dlHashC)) != 0 || slices.Contains(qb.started, dlHashC) {
		t.Fatalf("an untagged torrent was touched: %v %v", qb.tagsOf(dlHashC), qb.started)
	}
}

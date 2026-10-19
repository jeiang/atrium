package mediatools_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
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

	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/assistant/tool"
	mediatools "github.com/jeiang/atrium/internal/assistant/tools/media"
	"github.com/jeiang/atrium/internal/assistant/writes"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/db/dbtest"
	"github.com/jeiang/atrium/internal/media"
	"github.com/jeiang/atrium/internal/py"
)

const (
	now   = 1_800_000_000.0
	hashA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	hashB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	hashC = "cccccccccccccccccccccccccccccccccccccccc"
	hashD = "dddddddddddddddddddddddddddddddddddddddd"
)

type fakeFile struct {
	Index    int    `json:"index"`
	Name     string `json:"name"`
	Priority int    `json:"priority"`
}

type fakeTorrent struct {
	hash, name, state, contentPath string
	tags                           []string
	progress                       float64
	files                          []fakeFile
}

type addRecord struct {
	magnet, hash string
	tags         []string
}

type fakeQbit struct {
	mu      sync.Mutex
	order   []string
	items   map[string]*fakeTorrent
	added   []addRecord
	deleted []string
	writes  []string
	fail    int
	URL     string
}

func newFakeQbit(t *testing.T) *fakeQbit {
	t.Helper()
	f := &fakeQbit{items: map[string]*fakeTorrent{}}
	srv := httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(srv.Close)
	f.URL = srv.URL
	return f
}

func (f *fakeQbit) add(hash, name string, tags []string, state string, progress float64, contentPath string, files ...fakeFile) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.items[hash]; !ok {
		f.order = append(f.order, hash)
	}
	f.items[hash] = &fakeTorrent{hash: hash, name: name, tags: tags, state: state, progress: progress, contentPath: contentPath, files: files}
}

func (f *fakeQbit) tagsOf(hash string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.items[hash].tags)
}

func (f *fakeQbit) priorities(hash string) []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []int
	for _, file := range f.items[hash].files {
		out = append(out, file.Priority)
	}
	return out
}

func (f *fakeQbit) has(hash string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.items[hash]
	return ok
}

func (f *fakeQbit) writeLog() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.writes)
}

func (f *fakeQbit) handle(w http.ResponseWriter, r *http.Request) {
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
				"hash": t.hash, "name": t.name, "tags": strings.Join(t.tags, ", "), "state": t.state, "content_path": t.contentPath,
				"progress": t.progress, "size": 1000, "dlspeed": 0, "upspeed": 0, "eta": 8640000,
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
	form := formOf(r)
	f.writes = append(f.writes, path+" "+form.Get("hash")+form.Get("hashes")+form.Get("urls"))
	switch path {
	case "torrents/add":
		magnet := form.Get("urls")
		hash, _ := media.MagnetHash(magnet)
		tags := splitTags(form.Get("tags"))
		f.added = append(f.added, addRecord{magnet: magnet, hash: hash, tags: tags})
		if _, ok := f.items[hash]; !ok && hash != "" {
			f.order = append(f.order, hash)
			f.items[hash] = &fakeTorrent{hash: hash, name: "magnet " + hash[:8], state: "metaDL", tags: tags}
		}
		_, _ = io.WriteString(w, `{"added_torrent_ids":["x"],"failure_count":0,"pending_count":0,"success_count":1}`)
	case "torrents/addTags":
		for _, h := range strings.Split(form.Get("hashes"), "|") {
			if t, ok := f.items[h]; ok {
				for _, tag := range splitTags(form.Get("tags")) {
					if !slices.Contains(t.tags, tag) {
						t.tags = append(t.tags, tag)
					}
				}
			}
		}
	case "torrents/filePrio":
		t := f.items[form.Get("hash")]
		priority := 0
		if form.Get("priority") == "1" {
			priority = 1
		}
		for _, id := range strings.Split(form.Get("id"), "|") {
			for i := range t.files {
				if id == strconv.Itoa(t.files[i].Index) {
					t.files[i].Priority = priority
				}
			}
		}
	case "torrents/delete":
		for _, h := range strings.Split(form.Get("hashes"), "|") {
			f.deleted = append(f.deleted, h+" "+form.Get("deleteFiles"))
			delete(f.items, h)
			f.order = slices.DeleteFunc(f.order, func(o string) bool { return o == h })
		}
	case "torrents/start":
	default:
		http.NotFound(w, r)
	}
}

func splitTags(s string) []string {
	var out []string
	for _, tag := range strings.Split(s, ",") {
		if tag != "" {
			out = append(out, tag)
		}
	}
	return out
}

func formOf(r *http.Request) url.Values {
	mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err == nil && mediaType == "multipart/form-data" {
		out := url.Values{}
		reader := multipart.NewReader(r.Body, params["boundary"])
		for {
			part, err := reader.NextPart()
			if err != nil {
				return out
			}
			data, _ := io.ReadAll(part)
			out.Add(part.FormName(), string(data))
		}
	}
	body, _ := io.ReadAll(r.Body)
	values, _ := url.ParseQuery(string(body))
	return values
}

func newFakeTMDB(t *testing.T) {
	t.Helper()
	shows := map[string]string{
		"/tv/42":              `{"name": "Lanterns", "first_air_date": "2026-01-01"}`,
		"/tv/42/external_ids": `{"imdb_id": "tt1234567"}`,
		"/tv/43":              `{"name": "No IMDb", "first_air_date": "2026-01-01"}`,
		"/tv/43/external_ids": `{}`,
		"/find/tt1234567":     `{"tv_results": [{"id": 42, "name": "Lanterns", "first_air_date": "2026-01-01"}]}`,
		"/find/tt7654321":     `{"tv_results": []}`,
		"/find/tt1111111":     `{"tv_results": []}`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := shows[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	previous := media.TMDBAPI
	media.TMDBAPI = srv.URL
	t.Cleanup(func() { media.TMDBAPI = previous })
}

type world struct {
	t       *testing.T
	root    string
	qb      *fakeQbit
	d       *db.DB
	env     map[string]string
	tc      *tool.Context
	undoers *writes.Registry
}

func newWorld(t *testing.T) *world {
	t.Helper()
	root := t.TempDir()
	for _, d := range []string{"import", "movies", "tv", "downloads"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o777); err != nil {
			t.Fatal(err)
		}
	}
	newFakeTMDB(t)
	w := &world{t: t, root: root, qb: newFakeQbit(t), d: dbtest.Schema(t)}
	w.env = map[string]string{
		"ATRIUM_MEDIA_ROOT": root, "QBITTORRENT_URL": w.qb.URL, "ATRIUM_MEDIA_MOVIES_URL": "http://artemis.vpn:8084",
		"ATRIUM_MEDIA_TV_URL": "http://artemis.vpn:8085", "TMDB_API_KEY": "k",
	}
	thread, err := threads.Create(t.Context(), w.d.Write, 0, threads.NewThread{Kind: threads.Chat})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.d.Write.ExecContext(t.Context(), "INSERT INTO runs (id, thread_id, kind, model, effort, state, started_at) VALUES (1, ?, 'chat', 'm', 'low', 'running', 0)", thread); err != nil {
		t.Fatal(err)
	}
	w.tc = w.ctxWith(w.env, thread)
	w.undoers = writes.NewRegistry()
	mediatools.RegisterUndoer(w.undoers, config.Map(w.env))
	return w
}

func (w *world) ctxWith(env map[string]string, thread int64) *tool.Context {
	return &tool.Context{
		DB: w.d, Env: config.Map(env), Kind: tool.Chat, ThreadID: thread, RunID: 1,
		Now: func() float64 { return now },
	}
}

func (w *world) ctxWithout(keys ...string) *tool.Context {
	env := map[string]string{}
	for k, v := range w.env {
		if !slices.Contains(keys, k) {
			env[k] = v
		}
	}
	return w.ctxWith(env, w.tc.ThreadID)
}

func (w *world) completed(hash, name string, tags []string) {
	w.t.Helper()
	content := filepath.Join(w.root, "downloads", name)
	for _, f := range []string{name + ".mkv", "Sample/sample.mkv", "info.nfo"} {
		full := filepath.Join(content, f)
		if err := os.MkdirAll(filepath.Dir(full), 0o777); err != nil {
			w.t.Fatal(err)
		}
		if err := os.WriteFile(full, make([]byte, 100), 0o666); err != nil {
			w.t.Fatal(err)
		}
	}
	w.qb.add(hash, name, tags, media.StoppedUP, 1, content)
}

func (w *world) libraryFiles() {
	w.t.Helper()
	movie := filepath.Join(w.root, "movies", "Dune (2021)")
	season := filepath.Join(w.root, "tv", "Lanterns (2026)", "Season 01")
	for _, dir := range []string{movie, season} {
		if err := os.MkdirAll(dir, 0o777); err != nil {
			w.t.Fatal(err)
		}
	}
	for path, body := range map[string]string{
		filepath.Join(movie, "Dune (2021).mkv"):               "0123456789",
		filepath.Join(season, "Lanterns (2026) - S01E08.mkv"): "episode-bytes",
	} {
		if err := os.WriteFile(path, []byte(body), 0o666); err != nil {
			w.t.Fatal(err)
		}
	}
}

func (w *world) tool(name string) tool.Tool {
	w.t.Helper()
	for _, tl := range mediatools.Tools() {
		if tl.Name == name {
			return tl
		}
	}
	w.t.Fatalf("no tool %s", name)
	return tool.Tool{}
}

func loads(t *testing.T, text string) py.Dict {
	t.Helper()
	v, err := py.JSONLoads([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	return v.(py.Dict)
}

func (w *world) callWith(tc *tool.Context, name, args string) (py.Dict, error) {
	w.t.Helper()
	out, err := w.tool(name).Handler(context.Background(), tc, loads(w.t, args))
	if err != nil {
		return nil, err
	}
	d, ok := out.(py.Dict)
	if !ok {
		w.t.Fatalf("%s returned %T", name, out)
	}
	return d, nil
}

func (w *world) call(name, args string) py.Dict {
	w.t.Helper()
	out, err := w.callWith(w.tc, name, args)
	if err != nil {
		w.t.Fatalf("%s(%s): %v", name, args, err)
	}
	return out
}

func (w *world) undo(writeID int64) (any, error) {
	return w.undoers.Undo(context.Background(), w.d.Write, writeID, func() float64 { return now })
}

func get(t *testing.T, d py.Dict, key string) any {
	t.Helper()
	v, ok := d.Get(key)
	if !ok {
		t.Fatalf("no key %q in %s", key, py.JSONDumps(d))
	}
	return v
}

func list(t *testing.T, v any) []any {
	t.Helper()
	l, ok := v.([]any)
	if !ok {
		t.Fatalf("%T is not a list", v)
	}
	return l
}

func wantToolError(t *testing.T, err error, message string) {
	t.Helper()
	var te tool.Error
	if !errors.As(err, &te) {
		t.Fatalf("error %v (%T) is not a tool.Error", err, err)
	}
	if message != "" && te.Error() != message {
		t.Errorf("error %q, want %q", te, message)
	}
}

func wantNotConfigured(t *testing.T, err error) {
	t.Helper()
	var nc tool.NotConfigured
	if !errors.As(err, &nc) {
		t.Fatalf("error %v (%T) is not a tool.NotConfigured", err, err)
	}
}

func wantUndoError(t *testing.T, err error, message string) {
	t.Helper()
	var ue writes.UndoError
	if !errors.As(err, &ue) || ue.Error() != message {
		t.Fatalf("error %v, want undo error %q", err, message)
	}
}

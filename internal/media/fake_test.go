package media

import (
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
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
	kind, magnet, hash string
	tags               []string
}

type fakeQbit struct {
	mu      sync.Mutex
	order   []string
	items   map[string]*fakeTorrent
	added   []addRecord
	deleted []string
	started []string
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
	if contentPath == "" {
		contentPath = name
	}
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

func (f *fakeQbit) info(t *fakeTorrent) map[string]any {
	return map[string]any{
		"hash": t.hash, "name": t.name, "tags": strings.Join(t.tags, ", "), "state": t.state, "content_path": t.contentPath,
		"progress": t.progress, "size": 0, "dlspeed": 0, "upspeed": 0, "eta": 8640000,
	}
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
			rows = append(rows, f.info(t))
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
	f.writes = append(f.writes, path+" "+form.Get("hash")+form.Get("hashes"))
	switch path {
	case "torrents/add":
		magnet := form.Get("urls")
		hash, _ := MagnetHash(magnet)
		tags := splitTags(form.Get("tags"))
		f.added = append(f.added, addRecord{kind: "magnet", magnet: magnet, hash: hash, tags: tags})
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
		priority, _ := strconv.Atoi(form.Get("priority"))
		for _, id := range strings.Split(form.Get("id"), "|") {
			i, _ := strconv.Atoi(id)
			t.files[i].Priority = priority
		}
	case "torrents/delete":
		for _, h := range strings.Split(form.Get("hashes"), "|") {
			f.deleted = append(f.deleted, h+" "+form.Get("deleteFiles"))
			delete(f.items, h)
			f.order = slices.DeleteFunc(f.order, func(o string) bool { return o == h })
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

type fakeEztv struct {
	mu       sync.Mutex
	releases map[string][]map[string]any
	fail     int
}

func newFakeEztv(t *testing.T) *fakeEztv {
	t.Helper()
	f := &fakeEztv{releases: map[string][]map[string]any{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.fail != 0 {
			w.WriteHeader(f.fail)
			return
		}
		list := f.releases[r.URL.Query().Get("imdb_id")]
		if list == nil {
			list = []map[string]any{}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"torrents": list})
	}))
	t.Cleanup(srv.Close)
	previous := EZTVAPI
	EZTVAPI = srv.URL + "/api/get-torrents"
	t.Cleanup(func() { EZTVAPI = previous })
	return f
}

func (f *fakeEztv) release(imdb, hash, title, size string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.releases[imdb] = append(f.releases[imdb], map[string]any{
		"hash": hash, "title": title, "magnet_url": fmt.Sprintf("magnet:?xt=urn:btih:%s&dn=x", hash), "size_bytes": size, "season": "1", "episode": "1",
	})
}

type fakeNtfy struct {
	mu   sync.Mutex
	sent []map[string]any
	URL  string
}

func newFakeNtfy(t *testing.T) *fakeNtfy {
	t.Helper()
	f := &fakeNtfy{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.sent = append(f.sent, body)
		f.mu.Unlock()
	}))
	t.Cleanup(srv.Close)
	f.URL = srv.URL
	return f
}

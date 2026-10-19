package notion

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/py"
)

func uid(n int) string { return fmt.Sprintf("00000000-0000-0000-0000-%012d", n) }

func richJSON(text string) []any {
	return []any{py.Dict{{Key: "plain_text", Value: text}, {Key: "type", Value: "text"}, {Key: "text", Value: py.Dict{{Key: "content", Value: text}}}}}
}

func selectProp(name string) py.Dict {
	return py.Dict{{Key: "id", Value: "sel"}, {Key: "type", Value: "select"}, {Key: "select", Value: py.Dict{{Key: "name", Value: name}}}}
}

type fakeNotion struct {
	mu             sync.Mutex
	order          []string
	pages          map[string]py.Dict
	sourceOrder    []string
	sources        map[string]py.Dict
	markdown       map[string]string
	blocks         map[string][]py.Dict
	pageCap        int
	clock          int
	markdownStatus int
	requests       []string
}

func newFake(t *testing.T) (*fakeNotion, *httptest.Server) {
	t.Helper()
	f := &fakeNotion{pages: map[string]py.Dict{}, sources: map[string]py.Dict{}, markdown: map[string]string{}, blocks: map[string][]py.Dict{}, pageCap: 2}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeNotion) stamp() string {
	f.clock++
	return fmt.Sprintf("2026-01-01T00:%02d:%02d.000Z", f.clock/60, f.clock%60)
}

type pageOpt func(py.Dict) py.Dict

func parent(kind, id string) pageOpt {
	return func(p py.Dict) py.Dict {
		return p.Set("parent", py.Dict{{Key: "type", Value: kind}, {Key: kind, Value: id}})
	}
}

func withProp(name string, prop py.Dict) pageOpt {
	return func(p py.Dict) py.Dict {
		props := asDict(field(p, "properties")).Set(name, prop)
		return p.Set("properties", props)
	}
}

func (f *fakeNotion) addPage(id, title, markdown string, opts ...pageOpt) {
	page := py.Dict{
		{Key: "object", Value: "page"}, {Key: "id", Value: id}, {Key: "url", Value: "https://notion.invalid/" + id},
		{Key: "parent", Value: py.Dict{{Key: "type", Value: "workspace"}, {Key: "workspace", Value: true}}},
		{Key: "properties", Value: py.Dict{{Key: "Name", Value: py.Dict{{Key: "id", Value: "title"}, {Key: "type", Value: "title"}, {Key: "title", Value: richJSON(title)}}}}},
		{Key: "last_edited_time", Value: f.stamp()}, {Key: "in_trash", Value: false},
	}
	for _, o := range opts {
		page = o(page)
	}
	if _, ok := f.pages[id]; !ok {
		f.order = append(f.order, id)
	}
	f.pages[id] = page
	f.markdown[id] = markdown
}

func (f *fakeNotion) addSource(id, title, databaseID string) {
	f.sourceOrder = append(f.sourceOrder, id)
	f.sources[id] = py.Dict{
		{Key: "object", Value: "data_source"}, {Key: "id", Value: id}, {Key: "title", Value: richJSON(title)},
		{Key: "parent", Value: py.Dict{{Key: "type", Value: "database_id"}, {Key: "database_id", Value: databaseID}}},
		{Key: "database_parent", Value: py.Dict{{Key: "type", Value: "workspace"}, {Key: "workspace", Value: true}}},
		{Key: "properties", Value: py.Dict{}}, {Key: "url", Value: "https://notion.invalid/" + id},
		{Key: "last_edited_time", Value: f.stamp()}, {Key: "in_trash", Value: false},
	}
}

func (f *fakeNotion) touch(id string) { f.pages[id] = f.pages[id].Set("last_edited_time", f.stamp()) }

func (f *fakeNotion) setMarkdown(id, text string) {
	f.markdown[id] = text
	f.touch(id)
}

func (f *fakeNotion) setTrash(id string, trash bool) {
	f.pages[id] = f.pages[id].Set("in_trash", trash)
}

func (f *fakeNotion) remove(id string) {
	delete(f.pages, id)
	for i, o := range f.order {
		if o == id {
			f.order = append(f.order[:i], f.order[i+1:]...)
			break
		}
	}
}

func (f *fakeNotion) markdownRequests() int {
	n := 0
	for _, r := range f.requests {
		if strings.HasSuffix(r, "/markdown") {
			n++
		}
	}
	return n
}

func (f *fakeNotion) paged(items []py.Dict, cursor string, size int) py.Dict {
	start, _ := strconv.Atoi(cursor)
	step := f.pageCap
	if size > 0 {
		step = min(size, f.pageCap)
	}
	end := min(start+step, len(items))
	chunk := []any{}
	if start < len(items) {
		for _, item := range items[start:end] {
			chunk = append(chunk, item)
		}
	}
	more := start+step < len(items)
	var next any
	if more {
		next = strconv.Itoa(start + step)
	}
	return py.Dict{{Key: "object", Value: "list"}, {Key: "results", Value: chunk}, {Key: "has_more", Value: more}, {Key: "next_cursor", Value: next}}
}

func (f *fakeNotion) reply(w http.ResponseWriter, status int, payload any) {
	w.WriteHeader(status)
	_, _ = io.WriteString(w, py.JSONDumps(payload))
}

func (f *fakeNotion) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r.Method+" "+r.URL.Path)
	notFound := py.Dict{{Key: "code", Value: "object_not_found"}, {Key: "message", Value: "missing"}}
	switch {
	case r.URL.Path == "/v1/search":
		var body struct {
			Filter struct {
				Value   string `json:"value"`
				InTrash bool   `json:"in_trash"`
			} `json:"filter"`
			Cursor string `json:"start_cursor"`
			Size   int    `json:"page_size"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		var items []py.Dict
		if body.Filter.Value == "data_source" {
			for _, id := range f.sourceOrder {
				items = append(items, f.sources[id])
			}
		} else {
			for _, id := range f.order {
				if py.Truthy(field(f.pages[id], "in_trash")) == body.Filter.InTrash {
					items = append(items, f.pages[id])
				}
			}
		}
		f.reply(w, 200, f.paged(items, body.Cursor, body.Size))
	case strings.HasPrefix(r.URL.Path, "/v1/blocks/"):
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/blocks/"), "/children")
		cursor := r.URL.Query().Get("start_cursor")
		f.reply(w, 200, f.paged(f.blocks[id], cursor, 100))
	case strings.HasPrefix(r.URL.Path, "/v1/pages/"):
		rest := strings.TrimPrefix(r.URL.Path, "/v1/pages/")
		id, sub, _ := strings.Cut(rest, "/")
		page, ok := f.pages[id]
		switch {
		case !ok:
			f.reply(w, 404, notFound)
		case sub == "markdown" && f.markdownStatus != 0:
			f.reply(w, f.markdownStatus, py.Dict{{Key: "code", Value: "validation_error"}, {Key: "message", Value: "no"}})
		case sub == "markdown":
			f.reply(w, 200, py.Dict{{Key: "object", Value: "page_markdown"}, {Key: "id", Value: id}, {Key: "markdown", Value: f.markdown[id]}, {Key: "truncated", Value: false}})
		default:
			f.reply(w, 200, page)
		}
	default:
		f.reply(w, 404, notFound)
	}
}

func openMirror(t *testing.T, dir string) *db.DB {
	t.Helper()
	d, err := db.Connect(dir + "/atrium.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func mirrored(t *testing.T, dir, id string) Page {
	t.Helper()
	d := openMirror(t, dir)
	page, err := GetPage(t.Context(), d.Read, id)
	if err != nil || page == nil {
		t.Fatalf("GetPage(%s) = %v, %v", id, page, err)
	}
	return *page
}

func count(t *testing.T, dir, query string) int {
	t.Helper()
	d := openMirror(t, dir)
	var n int
	if err := d.Read.QueryRowContext(t.Context(), query).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

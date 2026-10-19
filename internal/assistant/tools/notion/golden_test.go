package notiontools_test

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/jeiang/atrium/internal/assistant/tool"
	notiontools "github.com/jeiang/atrium/internal/assistant/tools/notion"
	"github.com/jeiang/atrium/internal/assistant/toolset"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/db/dbtest"
	"github.com/jeiang/atrium/internal/notion"
	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/testutil/golden"
)

const now = 1_780_000_000.0

func uid(n int) string { return fmt.Sprintf("00000000-0000-0000-0000-%012d", n) }

var root, hub, alpha = uid(100), uid(2), uid(1)

type world struct {
	t   *testing.T
	d   *db.DB
	tc  *tool.Context
	env config.Env
}

func newWorld(t *testing.T, scenario string, existingLog bool) *world {
	t.Helper()
	srv := golden.Replay(t, golden.LoadScenario(t, "notion", scenario).Exchanges)
	previous := notiontools.NewClient
	notiontools.NewClient = func(token string) *notion.Client {
		c := notion.NewClient(token)
		c.BaseURL = srv.URL
		c.Clock = func() float64 { return 0 }
		c.Sleep = func(context.Context, float64) error { return nil }
		return c
	}
	t.Cleanup(func() { notiontools.NewClient = previous })
	d := dbtest.Schema(t)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := d.Write.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range [][3]string{{root, "Wiki Home", ""}, {hub, "Projects", root}, {alpha, "Alpha", hub}} {
		kind := "workspace"
		if p[2] != "" {
			kind = "page_id"
		}
		exec("INSERT INTO notion_pages (id, title, parent_type, parent_id, url) VALUES (?, ?, ?, ?, ?)", p[0], p[1], kind, p[2], "https://notion.invalid/"+p[0])
	}
	if existingLog {
		exec("INSERT INTO notion_pages (id, title, parent_type, parent_id, url) VALUES (?, ?, 'page_id', ?, ?)", uid(9), notion.LogTitle, root, "https://notion.invalid/"+uid(9))
	}
	exec("INSERT INTO threads (id, kind, effort, created_at, updated_at) VALUES (1, 'chat', 'low', 0, 0)")
	exec("INSERT INTO runs (id, thread_id, kind, model, effort, state, started_at) VALUES (1, 1, 'chat', 'm', 'low', 'running', 0)")
	env := config.Map(map[string]string{
		"NOTION_TOKEN": "golden-notion-token", "ATRIUM_NOTION_WIKI_ROOT": root, "ATRIUM_TZ": "America/Port_of_Spain",
		"ATRIUM_DB": filepath.Join(t.TempDir(), "atrium.db"), "ATRIUM_CACHE_DIR": t.TempDir(),
	})
	cfg, err := config.Load(env)
	if err != nil {
		t.Fatal(err)
	}
	return &world{t: t, d: d, env: env, tc: &tool.Context{
		DB: d, Cfg: cfg, Env: env, Kind: tool.Chat, ThreadID: 1, RunID: 1, Now: func() float64 { return now },
	}}
}

func (w *world) call(name string, args string) py.Dict {
	w.t.Helper()
	tools := map[string]tool.Tool{}
	for _, tl := range notiontools.Tools() {
		tools[tl.Name] = tl
	}
	parsed, err := py.JSONLoads([]byte(args))
	if err != nil {
		w.t.Fatal(err)
	}
	out, err := tools[name].Handler(w.t.Context(), w.tc, parsed.(py.Dict))
	if err != nil {
		w.t.Fatalf("%s: %v", name, err)
	}
	return out.(py.Dict)
}

func (w *world) undo(out py.Dict) {
	w.t.Helper()
	id, _ := out.Get("write_id")
	if _, err := toolset.Undoers(w.env).Undo(w.t.Context(), w.d.Write, id.(int64), func() float64 { return now }); err != nil {
		w.t.Fatal(err)
	}
}

func TestGoldenWikiCreatePage(t *testing.T) {
	w := newWorld(t, "wiki_create_page", false)
	out := w.call("notion_create_page", fmt.Sprintf(`{"parent_page_id": %q, "title": "Child", "markdown": "# hi", "aliases": ["kid"], "sources": ["chat 2026-10-17"]}`, hub))
	w.undo(out)
}

func TestGoldenWikiUpdatePage(t *testing.T) {
	w := newWorld(t, "wiki_update_page", false)
	w.call("notion_update_page", fmt.Sprintf(`{"page_id": %q, "section": "Status", "markdown_replace": "shipped"}`, alpha))
	out := w.call("notion_update_page", fmt.Sprintf(`{"page_id": %q, "append_markdown": "more", "sources": ["https://example.test/doc", "mail #12"]}`, alpha))
	w.undo(out)
}

func TestGoldenWikiMovePage(t *testing.T) {
	w := newWorld(t, "wiki_move_page", true)
	w.undo(w.call("notion_move_page", fmt.Sprintf(`{"page_id": %q, "parent_page_id": %q}`, alpha, root)))
}

func TestGoldenWikiArchivePage(t *testing.T) {
	w := newWorld(t, "wiki_archive_page", true)
	w.undo(w.call("notion_archive_page", fmt.Sprintf(`{"page_id": %q}`, alpha)))
}

func TestGoldenLiveFetch(t *testing.T) {
	w := newWorld(t, "live_fetch", false)
	w.call("fetch_knowledge", fmt.Sprintf(`{"page_id": %q}`, alpha))
}

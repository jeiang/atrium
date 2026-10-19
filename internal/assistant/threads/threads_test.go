package threads_test

import (
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/jeiang/atrium/internal/assistant/budget"
	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/db/dbtest"
)

func ptr[T any](v T) *T { return &v }

func create(t *testing.T, d *db.DB, n threads.NewThread) int64 {
	t.Helper()
	id, err := threads.Create(t.Context(), d.Write, 1, n)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func get(t *testing.T, d *db.DB, id int64) *threads.Thread {
	t.Helper()
	th, err := threads.Get(t.Context(), d.Read, id)
	if err != nil || th == nil {
		t.Fatalf("Get(%d) = %v, %v", id, th, err)
	}
	return th
}

func TestCleanOptions(t *testing.T) {
	for _, bad := range []string{"", "ultra", "LOW"} {
		if _, err := threads.CleanEffort(bad); err == nil || err.Error() != "effort must be one of low, medium, high, xhigh, max" {
			t.Errorf("CleanEffort(%q) = %v", bad, err)
		}
	}
	for in, want := range map[any]int64{90_000: 90_000, "200000": 200_000, int64(500_000): 500_000, 1e6: 1_000_000, " 90_000 ": 90_000, 200000.9: 200_000} {
		if got, err := threads.CleanContextLimit(in); err != nil || got != want {
			t.Errorf("CleanContextLimit(%v) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []any{nil, "", "abc", 12, "1e5", 100_000.5e10, []int{1}, true} {
		_, err := threads.CleanContextLimit(in)
		var oe threads.OptionError
		if !errors.As(err, &oe) || err.Error() != "context limit must be one of 90K, 200K, 500K, 1000K" {
			t.Errorf("CleanContextLimit(%v) = %v", in, err)
		}
	}
}

func TestDefaultsAndCreate(t *testing.T) {
	d := dbtest.Schema(t)
	ctx := t.Context()
	got, err := threads.Defaults(ctx, d.Read)
	if err != nil || got != (threads.Options{Effort: "medium", ContextLimit: 90_000}) {
		t.Fatalf("defaults = %+v, %v", got, err)
	}
	if err := threads.SetDefaults(ctx, d.Write, "high", "500000"); err != nil {
		t.Fatal(err)
	}
	if got, _ := threads.Defaults(ctx, d.Read); got != (threads.Options{Effort: "high", ContextLimit: 500_000}) {
		t.Errorf("after SetDefaults: %+v", got)
	}
	if err := threads.SetDefaults(ctx, d.Write, "bogus", 90_000); err == nil {
		t.Error("an unknown effort was stored")
	}
	if err := budget.SetSetting(ctx, d.Write, threads.EffortKey, "bogus"); err != nil {
		t.Fatal(err)
	}
	if err := budget.SetSetting(ctx, d.Write, threads.ContextLimitKey, "12345"); err != nil {
		t.Fatal(err)
	}
	if got, _ := threads.Defaults(ctx, d.Read); got != (threads.Options{Effort: "medium", ContextLimit: 90_000}) {
		t.Errorf("invalid stored defaults fall back: %+v", got)
	}

	chat := get(t, d, create(t, d, threads.NewThread{Kind: threads.Chat, Title: ptr("hi")}))
	if chat.Effort != "medium" || *chat.ContextLimit != 90_000 || *chat.Title != "hi" || chat.AllowedURLs == nil || len(chat.AllowedURLs) != 0 {
		t.Errorf("chat thread %+v", chat)
	}
	for kind, effort := range map[string]string{threads.Cron: "high", threads.Nightly: "high", threads.Sub: "medium", threads.Subagent: "medium"} {
		if th := get(t, d, create(t, d, threads.NewThread{Kind: kind})); th.Effort != effort {
			t.Errorf("%s thread effort %s, want %s", kind, th.Effort, effort)
		}
	}
	if _, err := threads.Create(ctx, d.Write, 1, threads.NewThread{Kind: "code"}); err == nil {
		t.Error("a kind without a fixed effort was created")
	}
	if _, err := threads.Create(ctx, d.Write, 1, threads.NewThread{Kind: threads.Chat, ProjectID: ptr(int64(99))}); err == nil {
		t.Error("a thread was created in a project that does not exist")
	}
}

func TestProjectThreadsTakeTheProjectDefaults(t *testing.T) {
	d := dbtest.Schema(t)
	ctx := t.Context()
	if _, err := d.Write.ExecContext(ctx, "INSERT INTO projects (id, name, effort, context_limit, created_at, updated_at) VALUES (1, 'Kitchen', 'xhigh', 500000, 0, 0)"); err != nil {
		t.Fatal(err)
	}
	in := get(t, d, create(t, d, threads.NewThread{Kind: threads.Chat, ProjectID: ptr(int64(1))}))
	if in.Effort != "xhigh" || *in.ContextLimit != 500_000 || *in.ProjectID != 1 {
		t.Errorf("project thread %+v", in)
	}
	plain := get(t, d, create(t, d, threads.NewThread{Kind: threads.Chat}))
	if plain.Effort != "medium" || *plain.ContextLimit != 90_000 || plain.ProjectID != nil {
		t.Errorf("plain thread %+v", plain)
	}
	cron := get(t, d, create(t, d, threads.NewThread{Kind: threads.Cron, ProjectID: ptr(int64(1))}))
	if cron.Effort != "high" || *cron.ContextLimit != 500_000 {
		t.Errorf("a cron thread in a project keeps its kind's effort and takes the limit: %+v", cron)
	}
}

func TestSetOptionsOnlyChangesChats(t *testing.T) {
	d := dbtest.Schema(t)
	ctx := t.Context()
	chat := create(t, d, threads.NewThread{Kind: threads.Chat})
	cron := create(t, d, threads.NewThread{Kind: threads.Cron})
	if ok, err := threads.SetOptions(ctx, d.Write, chat, "max", "1000000"); err != nil || !ok {
		t.Fatalf("SetOptions(chat) = %v, %v", ok, err)
	}
	if th := get(t, d, chat); th.Effort != "max" || *th.ContextLimit != 1_000_000 {
		t.Errorf("%+v", th)
	}
	if ok, err := threads.SetOptions(ctx, d.Write, cron, "low", 90_000); err != nil || ok {
		t.Errorf("SetOptions(cron) = %v, %v", ok, err)
	}
	if _, err := threads.SetOptions(ctx, d.Write, chat, "low", 7); err == nil {
		t.Error("an invalid limit was accepted")
	}
	if got := threads.ContextLimitOf(threads.Thread{}); got != threads.DefaultContextLimit {
		t.Errorf("ContextLimitOf(none) = %d", got)
	}
	zero := int64(0)
	if got := threads.ContextLimitOf(threads.Thread{ContextLimit: &zero}); got != threads.DefaultContextLimit {
		t.Errorf("ContextLimitOf(0) = %d", got)
	}
	if got := threads.ContextLimitOf(*get(t, d, chat)); got != 1_000_000 {
		t.Errorf("ContextLimitOf = %d", got)
	}
}

func TestTitleFrom(t *testing.T) {
	long := strings.Repeat("é", 100)
	for in, want := range map[string]string{
		"":                           "",
		"  \n\n  ":                   "",
		"\n\n  hello world \nsecond": "hello world",
		strings.Repeat("a", 80):      strings.Repeat("a", 80),
		strings.Repeat("a", 81):      strings.Repeat("a", 79) + "…",
		long:                         strings.Repeat("é", 79) + "…",
	} {
		if got := threads.TitleFrom(in); got != want {
			t.Errorf("TitleFrom(%.20q) = %q, want %q", in, got, want)
		}
	}
}

func TestSetTitleOnlyNamesUntitledThreads(t *testing.T) {
	d := dbtest.Schema(t)
	ctx := t.Context()
	id := create(t, d, threads.NewThread{Kind: threads.Chat})
	if err := threads.SetTitle(ctx, d.Write, id, "First"); err != nil {
		t.Fatal(err)
	}
	if err := threads.SetTitle(ctx, d.Write, id, "Second"); err != nil {
		t.Fatal(err)
	}
	if th := get(t, d, id); *th.Title != "First" {
		t.Errorf("title %q", *th.Title)
	}
	var n int
	if err := d.Read.QueryRowContext(ctx, "SELECT COUNT(*) FROM thread_fts WHERE thread_id = ?", id).Scan(&n); err != nil || n != 1 {
		t.Errorf("fts rows %d, %v", n, err)
	}
	listed, err := threads.ListThreads(ctx, d.Read, threads.ListOpts{Match: `"firs"*`, Limit: 10})
	if err != nil || len(listed) != 1 || listed[0].ID != id {
		t.Errorf("search by title: %+v, %v", listed, err)
	}
}

func TestRootKindFollowsParentsAndStopsOnLoops(t *testing.T) {
	d := dbtest.Schema(t)
	ctx := t.Context()
	cron := create(t, d, threads.NewThread{Kind: threads.Cron})
	sub := create(t, d, threads.NewThread{Kind: threads.Subagent, ParentID: &cron})
	grand := create(t, d, threads.NewThread{Kind: threads.Sub, ParentID: &sub})
	for id, want := range map[int64]string{cron: "cron", sub: "cron", grand: "cron", 999: ""} {
		if got, err := threads.RootKind(ctx, d.Read, id); err != nil || got != want {
			t.Errorf("RootKind(%d) = %q, %v; want %q", id, got, err, want)
		}
	}
	if _, err := d.Write.ExecContext(ctx, "UPDATE threads SET parent_thread_id = ? WHERE id = ?", grand, cron); err != nil {
		t.Fatal(err)
	}
	if got, err := threads.RootKind(ctx, d.Read, grand); err != nil || got == "" {
		t.Errorf("RootKind in a cycle = %q, %v", got, err)
	}
}

func TestAllowedURLs(t *testing.T) {
	d := dbtest.Schema(t)
	ctx := t.Context()
	id := create(t, d, threads.NewThread{Kind: threads.Chat})
	if got, err := threads.AllowedURLs(ctx, d.Read, 999); err != nil || len(got) != 0 {
		t.Errorf("AllowedURLs(unknown) = %v, %v", got, err)
	}
	if err := threads.AddAllowedURLs(ctx, d, id, []string{"", "https://b.test/é", "https://a.test"}); err != nil {
		t.Fatal(err)
	}
	if err := threads.AddAllowedURLs(ctx, d, id, []string{"https://a.test", "https://c.test"}); err != nil {
		t.Fatal(err)
	}
	if err := threads.AddAllowedURLs(ctx, d, id, nil); err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := d.Read.QueryRowContext(ctx, "SELECT allowed_urls FROM threads WHERE id = ?", id).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if want := `["https://a.test", "https://b.test/\u00e9", "https://c.test"]`; raw != want {
		t.Errorf("allowed_urls = %s, want %s", raw, want)
	}
	if th := get(t, d, id); !slices.Equal(th.AllowedURLs, []string{"https://a.test", "https://b.test/é", "https://c.test"}) {
		t.Errorf("thread urls %v", th.AllowedURLs)
	}
}

func TestAddAllowedURLsInParallel(t *testing.T) {
	d := dbtest.Schema(t)
	ctx := t.Context()
	id := create(t, d, threads.NewThread{Kind: threads.Chat})
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := threads.AddAllowedURLs(ctx, d, id, []string{"https://" + string(rune('a'+i)) + ".test"}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	got, err := threads.AllowedURLs(ctx, d.Read, id)
	if err != nil || len(got) != 8 {
		t.Errorf("allowed %v, %v", got, err)
	}
}

func TestURLsIn(t *testing.T) {
	got := threads.URLsIn("see https://a.test/x?y=1, and (http://b.test/z). Also <https://c.test/>; \"https://d.test\"! https://e.test/a.b.")
	want := []string{"https://a.test/x?y=1", "http://b.test/z", "https://c.test/", "https://d.test", "https://e.test/a.b"}
	if !slices.Equal(got, want) {
		t.Errorf("URLsIn = %q, want %q", got, want)
	}
	if got := threads.URLsIn(""); len(got) != 0 {
		t.Errorf("URLsIn(empty) = %q", got)
	}
}

func TestListAndFlags(t *testing.T) {
	d := dbtest.Schema(t)
	ctx := t.Context()
	older := create(t, d, threads.NewThread{Kind: threads.Chat, Title: ptr("Older")})
	newer := create(t, d, threads.NewThread{Kind: threads.Chat, Title: ptr("Newer")})
	cron := create(t, d, threads.NewThread{Kind: threads.Cron})
	if _, err := d.Write.ExecContext(ctx, "UPDATE threads SET updated_at = id"); err != nil {
		t.Fatal(err)
	}
	ids := func(o threads.ListOpts) []int64 {
		t.Helper()
		o.Limit = 50
		rows, err := threads.ListThreads(ctx, d.Read, o)
		if err != nil {
			t.Fatal(err)
		}
		var out []int64
		for _, r := range rows {
			out = append(out, r.ID)
		}
		return out
	}
	if got := ids(threads.ListOpts{}); !slices.Equal(got, []int64{newer, older}) {
		t.Errorf("list = %v (cron %d must be hidden)", got, cron)
	}
	if ok, err := threads.SetFlag(ctx, d.Write, older, "pinned", true); err != nil || !ok {
		t.Fatalf("pin = %v, %v", ok, err)
	}
	if got := ids(threads.ListOpts{}); !slices.Equal(got, []int64{older, newer}) {
		t.Errorf("pinned first: %v", got)
	}
	if ok, err := threads.SetFlag(ctx, d.Write, cron, "pinned", true); err != nil || ok {
		t.Errorf("pinning a cron thread = %v, %v", ok, err)
	}
	if _, err := threads.SetFlag(ctx, d.Write, older, "title", true); err == nil {
		t.Error("an arbitrary column was set")
	}
	if ok, err := threads.SetFlag(ctx, d.Write, newer, "archived", true); err != nil || !ok {
		t.Fatal(err)
	}
	if got := ids(threads.ListOpts{}); !slices.Equal(got, []int64{older}) {
		t.Errorf("archived hidden: %v", got)
	}
	if got := ids(threads.ListOpts{Archived: true}); !slices.Equal(got, []int64{newer}) {
		t.Errorf("archived list: %v", got)
	}
	if got := ids(threads.ListOpts{ProjectID: 5}); len(got) != 0 {
		t.Errorf("project filter: %v", got)
	}
	if _, err := d.Write.ExecContext(ctx, "INSERT INTO projects (id, name, effort, context_limit, created_at, updated_at) VALUES (5, 'P', 'low', 90000, 0, 0)"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.ExecContext(ctx, "UPDATE threads SET project_id = 5 WHERE id = ?", older); err != nil {
		t.Fatal(err)
	}
	rows, err := threads.ListThreads(ctx, d.Read, threads.ListOpts{ProjectID: 5, Limit: 10})
	if err != nil || len(rows) != 1 || rows[0].ProjectName == nil || *rows[0].ProjectName != "P" || rows[0].RunState != nil || rows[0].Cost != 0 {
		t.Errorf("project list %+v, %v", rows, err)
	}
	if err := threads.SetContainer(ctx, d.Write, older, ptr("c1")); err != nil {
		t.Fatal(err)
	}
	if th := get(t, d, older); *th.ContainerID != "c1" || !bool(th.Pinned) {
		t.Errorf("%+v", th)
	}
}

func TestPurgeKeepsATombstone(t *testing.T) {
	d := dbtest.Schema(t)
	ctx := t.Context()
	id := create(t, d, threads.NewThread{Kind: threads.Chat, Title: ptr("Secret")})
	for _, stmt := range []string{
		"INSERT INTO runs (id, thread_id, kind, model, effort, state, started_at) VALUES (1, 1, 'chat', 'm', 'low', 'done', 0)",
		"INSERT INTO thread_messages (thread_id, run_id, seq, role, content_json, created_at) VALUES (1, 1, 1, 'user', '[]', 0)",
		"INSERT INTO thread_fts (thread_id, text) VALUES (1, 'secret')",
		"INSERT INTO thread_queue (thread_id, text, created_at) VALUES (1, 'q', 0)",
		"INSERT INTO thread_plans (thread_id, items_json, updated_at) VALUES (1, '[]', 0)",
		"INSERT INTO tool_calls (id, run_id, tool_use_id, name, args_json, args_sha256, replay_class, state, started_at) VALUES (1, 1, 'tu', 'n', '{}', 'x', 'safe', 'done', 0)",
		"INSERT INTO usage (ts, thread_id, run_id, kind, model, effort, input_tokens, cache_write_5m, cache_write_1h, cache_read, output_tokens, searches, tier, cost_usd) VALUES (0, 1, 1, 'chat', 'm', 'low', 0, 0, 0, 0, 0, 0, '<=100K', 1.5)",
	} {
		if _, err := d.Write.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	if err := threads.Purge(ctx, d.Write, id, 123); err != nil {
		t.Fatal(err)
	}
	if th, err := threads.Get(ctx, d.Read, id); err != nil || th != nil {
		t.Errorf("purged thread still visible: %+v, %v", th, err)
	}
	for table, want := range map[string]int{"thread_messages": 0, "thread_fts": 0, "thread_queue": 0, "thread_plans": 0, "tool_calls": 0, "runs": 1, "usage": 1, "threads": 1} {
		var n int
		if err := d.Read.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&n); err != nil || n != want {
			t.Errorf("%s rows = %d, %v; want %d", table, n, err, want)
		}
	}
	var deleted, archived, pinned int
	var title *string
	if err := d.Read.QueryRowContext(ctx, "SELECT deleted_at, archived, pinned, title FROM threads WHERE id = ?", id).Scan(&deleted, &archived, &pinned, &title); err != nil ||
		deleted != 123 || archived != 1 || pinned != 0 || title != nil {
		t.Errorf("tombstone %d %d %d %v, %v", deleted, archived, pinned, title, err)
	}
	if rows, err := threads.ListThreads(ctx, d.Read, threads.ListOpts{Archived: true, Limit: 10}); err != nil || len(rows) != 0 {
		t.Errorf("a tombstone listed: %+v, %v", rows, err)
	}
}

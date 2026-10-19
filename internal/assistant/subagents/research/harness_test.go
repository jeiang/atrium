package research_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/assistant/journal"
	"github.com/jeiang/atrium/internal/assistant/loop"
	"github.com/jeiang/atrium/internal/assistant/registry"
	"github.com/jeiang/atrium/internal/assistant/registry/registrytest"
	"github.com/jeiang/atrium/internal/assistant/service"
	"github.com/jeiang/atrium/internal/assistant/skills"
	"github.com/jeiang/atrium/internal/assistant/subagents/research"
	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/assistant/tool"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/db/dbtest"
	"github.com/jeiang/atrium/internal/py"
)

const (
	zone = "America/Port_of_Spain"
	now0 = 1_800_000_000.0
	// digest is the text of the child's answer in the Python tests.
	digest = "Findings:\n- Invoice due Friday [msg-42]\nUncertainty: none found for March."
)

var expected = map[string][]string{
	"web":      {"recall", "web_fetch"},
	"mail":     {"contacts_lookup", "mail_read", "mail_search", "recall"},
	"notion":   {"fetch_knowledge", "recall", "search_knowledge"},
	"calendar": {"calendar_list", "recall"},
}

// clock is a thread-safe clock, one millisecond later at every call.
func clock() func() float64 {
	var mu sync.Mutex
	t := now0
	return func() float64 {
		mu.Lock()
		defer mu.Unlock()
		t += 0.001
		return t
	}
}

type world struct {
	t      *testing.T
	d      *db.DB
	cfg    config.Config
	env    config.Env
	srv    *anthropictest.Server
	reg    *registry.Registry
	runner *loop.Runner
	svc    *service.Service
	now    func() float64
}

// newWorld is a migrated database and a registry of the recorded tool definitions (stubs, replaced by
// overrides) with the real research tool installed, run by a Runner with the production skills hooks.
func newWorld(t *testing.T, overrides map[string]tool.Handler) *world {
	t.Helper()
	d := dbtest.Schema(t)
	dir := t.TempDir()
	env := config.Map(map[string]string{
		"ATRIUM_DB": filepath.Join(dir, "atrium.db"), "ATRIUM_CACHE_DIR": filepath.Join(dir, "cache"), "ATRIUM_MODE": "shadow", "ATRIUM_TZ": zone,
	})
	cfg, err := config.Load(env)
	if err != nil {
		t.Fatal(err)
	}
	reg := registry.New()
	for _, stub := range registrytest.Load(t).Stubs(t) {
		if stub.Name == research.Name {
			continue
		}
		if h := overrides[stub.Name]; h != nil {
			stub.Handler = h
		}
		if err := reg.Add(stub); err != nil {
			t.Fatal(err)
		}
	}
	runner := &loop.Runner{Registry: reg, SkillsIndex: skills.IndexBlock, ExpandSkills: skills.ExpandHook}
	rs := research.New(runner)
	if err := rs.Install(reg); err != nil {
		t.Fatal(err)
	}
	runner.ResearchBatch = rs.Batch
	srv := anthropictest.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	svc := service.New(ctx, d, cfg, env, runner)
	svc.Client = srv.Client()
	svc.Now = clock()
	t.Cleanup(func() {
		svc.Close()
		cancel()
	})
	return &world{t: t, d: d, cfg: cfg, env: env, srv: srv, reg: reg, runner: runner, svc: svc, now: svc.Now}
}

// parent is the Python helper of that name: a chat thread with a running run and the ToolContext of its tools.
func (w *world) parent() (thread, run int64, tc *tool.Context) {
	w.t.Helper()
	ctx := w.t.Context()
	thread, err := service.CreateThread(ctx, w.d.Write, w.now(), threads.NewThread{Kind: threads.Chat})
	if err != nil {
		w.t.Fatal(err)
	}
	run, err = journal.StartRun(ctx, w.d.Write, thread, "chat", "m", "medium", w.now(), nil, nil)
	if err != nil {
		w.t.Fatal(err)
	}
	return thread, run, &tool.Context{
		DB: w.d, Cfg: w.cfg, Env: w.env, Kind: tool.Chat, ThreadID: thread, RunID: run,
		AllowedURLs: map[string]struct{}{}, Now: w.now, Client: w.srv.Client(),
	}
}

// handler is the registered research tool's handler.
func (w *world) handler() tool.Handler {
	w.t.Helper()
	all, err := w.reg.All()
	if err != nil {
		w.t.Fatal(err)
	}
	for _, tl := range all {
		if tl.Name == research.Name {
			return tl.Handler
		}
	}
	w.t.Fatal("no research tool")
	return nil
}

func (w *world) research(tc *tool.Context, args py.Dict) (any, error) {
	return w.handler()(w.t.Context(), tc, args)
}

func (w *world) reply(stop string, usage *anthropictest.Usage, content ...json.RawMessage) {
	r := anthropictest.Reply(stop, content...)
	if usage != nil {
		r = r.WithUsage(*usage)
	}
	w.srv.Push(r)
}

func (w *world) thread(id int64) *threads.Thread {
	w.t.Helper()
	found, err := threads.Get(w.t.Context(), w.d.Read, id)
	if err != nil || found == nil {
		w.t.Fatalf("thread %d: %v", id, err)
	}
	return found
}

func (w *world) runs(thread int64) []journal.Run {
	w.t.Helper()
	runs, err := journal.RunsOf(w.t.Context(), w.d.Read, thread)
	if err != nil {
		w.t.Fatal(err)
	}
	return runs
}

func (w *world) count(query string, args ...any) int {
	w.t.Helper()
	var n int
	if err := w.d.Read.QueryRow(query, args...).Scan(&n); err != nil {
		w.t.Fatal(err)
	}
	return n
}

func (w *world) enabled(name string, on bool) {
	w.t.Helper()
	v := 0
	if on {
		v = 1
	}
	if _, err := w.d.Write.Exec("UPDATE subagents SET enabled = ? WHERE name = ?", v, name); err != nil {
		w.t.Fatal(err)
	}
}

func dict(t *testing.T, v any) py.Dict {
	t.Helper()
	d, ok := v.(py.Dict)
	if !ok {
		t.Fatalf("result %T is not a dict", v)
	}
	return d
}

func get(d py.Dict, key string) any {
	v, _ := d.Get(key)
	return v
}

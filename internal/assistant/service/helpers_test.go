package service_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/assistant/journal"
	"github.com/jeiang/atrium/internal/assistant/loop"
	"github.com/jeiang/atrium/internal/assistant/registry"
	"github.com/jeiang/atrium/internal/assistant/service"
	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/assistant/tool"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/db/dbtest"
	"github.com/jeiang/atrium/internal/py"
)

const zone = "America/Port_of_Spain"

// clock is the tests' clock(start): each call is one second later, the first start+1.
func clock(start float64) func() float64 {
	t := start
	return func() float64 {
		t++
		return t
	}
}

func wall(year int, month time.Month, day, hour, minute int) float64 {
	loc, err := time.LoadLocation(zone)
	if err != nil {
		panic(err)
	}
	return float64(time.Date(year, month, day, hour, minute, 0, 0, loc).Unix())
}

type world struct {
	t      *testing.T
	d      *db.DB
	cfg    config.Config
	srv    *anthropictest.Server
	svc    *service.Service
	runner *loop.Runner
}

func newWorld(t *testing.T, tools ...tool.Tool) *world {
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
	if err := reg.Add(tools...); err != nil {
		t.Fatal(err)
	}
	srv := anthropictest.New(t)
	runner := &loop.Runner{Registry: reg}
	ctx, cancel := context.WithCancel(context.Background())
	svc := service.New(ctx, d, cfg, env, runner)
	t.Cleanup(func() {
		svc.Close()
		cancel()
	})
	svc.Client = srv.Client()
	return &world{t: t, d: d, cfg: cfg, srv: srv, svc: svc, runner: runner}
}

func (w *world) thread(kind string) int64 {
	w.t.Helper()
	id, err := service.CreateThread(w.t.Context(), w.d.Write, 1_780_000_000, threads.NewThread{Kind: kind})
	if err != nil {
		w.t.Fatal(err)
	}
	return id
}

// chat is run_chat: a new thread of kind and one run in the caller.
func (w *world) chat(text, kind string) (int64, int64) {
	w.t.Helper()
	thread := w.thread(kind)
	return thread, w.turn(thread, text, kind)
}

func (w *world) turn(thread int64, text, kind string) int64 {
	w.t.Helper()
	w.svc.Now = clock(1_780_000_000)
	run, err := w.svc.RunOnce(w.t.Context(), thread, text, service.Start{Kind: kind})
	if err != nil {
		w.t.Fatal(err)
	}
	return run
}

func (w *world) run(id int64) *journal.Run {
	w.t.Helper()
	r, err := journal.GetRun(w.t.Context(), w.d.Read, id)
	if err != nil || r == nil {
		w.t.Fatalf("run %d: %v", id, err)
	}
	return r
}

func (w *world) messages(thread int64) []journal.Message {
	w.t.Helper()
	m, err := journal.MessagesOf(w.t.Context(), w.d.Read, thread, false)
	if err != nil {
		w.t.Fatal(err)
	}
	return m
}

func (w *world) threadRow(id int64) *threads.Thread {
	w.t.Helper()
	found, err := threads.Get(w.t.Context(), w.d.Read, id)
	if err != nil || found == nil {
		w.t.Fatalf("thread %d: %v", id, err)
	}
	return found
}

// body is request i as a decoded JSON object.
func (w *world) body(i int) map[string]any {
	w.t.Helper()
	reqs := w.srv.Requests()
	if i >= len(reqs) {
		w.t.Fatalf("request %d of %d", i, len(reqs))
	}
	return reqs[i].JSON(w.t)
}

func (w *world) push(replies ...anthropictest.Response) { w.srv.Push(replies...) }

// probe is a read tool offered in chat, cron and nightly, like the Python tests' Tool defaults.
func probe(name string, effect tool.Effect, h tool.Handler) tool.Tool {
	return tool.Tool{Name: name, Description: name, InputSchema: json.RawMessage(`{"type":"object"}`), Handler: h, Effect: effect, Contexts: tool.DefaultKinds}
}

func echo(_ context.Context, _ *tool.Context, args py.Dict) (any, error) {
	x, _ := args.Get("x")
	return py.Dict{{Key: "echo", Value: x}}, nil
}

func reply(blocks ...json.RawMessage) anthropictest.Response {
	return anthropictest.Reply("end_turn", blocks...)
}

func toolTurn(blocks ...json.RawMessage) anthropictest.Response {
	return anthropictest.Reply("tool_use", blocks...)
}

func text(s string) json.RawMessage { return anthropictest.Text(s) }

func use(id, name string, input map[string]any) json.RawMessage {
	return anthropictest.ToolUse(id, name, input)
}

// path walks a decoded JSON value by object keys and list indexes.
func path(v any, steps ...any) any {
	for _, s := range steps {
		switch k := s.(type) {
		case string:
			m, _ := v.(map[string]any)
			v = m[k]
		case int:
			l, _ := v.([]any)
			if k < 0 {
				k += len(l)
			}
			if k < 0 || k >= len(l) {
				return nil
			}
			v = l[k]
		}
	}
	return v
}

func blocksOf(content []json.RawMessage) []map[string]any {
	out := make([]map[string]any, len(content))
	for i, raw := range content {
		_ = json.Unmarshal(raw, &out[i])
	}
	return out
}

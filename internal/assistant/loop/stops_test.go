package loop_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/assistant/journal"
	"github.com/jeiang/atrium/internal/assistant/runner"
	"github.com/jeiang/atrium/internal/assistant/service"
	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/db/dbtest"
)

const tz = "America/Port_of_Spain"

var noon = func() float64 {
	loc, err := time.LoadLocation(tz)
	if err != nil {
		panic(err)
	}
	return float64(time.Date(2026, 10, 15, 12, 0, 0, 0, loc).Unix())
}()

type world struct {
	t   *testing.T
	d   *db.DB
	srv *anthropictest.Server
	svc *service.Service
}

func newWorld(t *testing.T) *world {
	t.Helper()
	d := dbtest.Schema(t)
	dir := t.TempDir()
	env := config.Map(map[string]string{
		"ATRIUM_DB": filepath.Join(dir, "atrium.db"), "ATRIUM_CACHE_DIR": filepath.Join(dir, "cache"), "ATRIUM_MODE": "shadow", "ATRIUM_TZ": tz,
	})
	cfg, err := config.Load(env)
	if err != nil {
		t.Fatal(err)
	}
	r, err := runner.New()
	if err != nil {
		t.Fatal(err)
	}
	srv := anthropictest.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	svc := service.New(ctx, d, cfg, env, r)
	svc.Client = srv.Client()
	t.Cleanup(func() {
		svc.Close()
		cancel()
	})
	tick := noon
	svc.Now = func() float64 { tick += 0.001; return tick }
	return &world{t: t, d: d, srv: srv, svc: svc}
}

type outcome struct {
	run      *journal.Run
	thread   int64
	messages []journal.Message
	usage    int
}

func (w *world) turn(script ...anthropictest.Response) outcome {
	w.t.Helper()
	ctx := w.t.Context()
	thread, err := service.CreateThread(ctx, w.d.Write, w.svc.Now(), threads.NewThread{Kind: threads.Chat})
	if err != nil {
		w.t.Fatal(err)
	}
	w.srv.Push(script...)
	id, err := w.svc.RunOnce(ctx, thread, "go", service.Start{Kind: threads.Chat})
	if err != nil {
		w.t.Fatal(err)
	}
	if w.srv.Remaining() != 0 {
		w.t.Fatalf("%d scripted replies unused", w.srv.Remaining())
	}
	run, err := journal.GetRun(ctx, w.d.Read, id)
	if err != nil || run == nil {
		w.t.Fatal(err)
	}
	messages, err := journal.MessagesOf(ctx, w.d.Read, thread, false)
	if err != nil {
		w.t.Fatal(err)
	}
	var rows int
	if err := w.d.Read.QueryRowContext(ctx, "SELECT COUNT(*) FROM usage WHERE run_id = ?", id).Scan(&rows); err != nil {
		w.t.Fatal(err)
	}
	return outcome{run: run, thread: thread, messages: messages, usage: rows}
}

func (o outcome) last(t *testing.T) journal.Message {
	t.Helper()
	return o.messages[len(o.messages)-1]
}

func (o outcome) assertFailed(t *testing.T, want string) {
	t.Helper()
	if o.run.State != journal.Failed || o.run.Error == nil || !strings.Contains(*o.run.Error, want) {
		t.Fatalf("run %s %v, want failed containing %q", o.run.State, o.run.Error, want)
	}
}

func blockTypes(t *testing.T, m journal.Message) []string {
	t.Helper()
	var types []string
	for _, raw := range m.Content {
		var b struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(raw, &b); err != nil {
			t.Fatal(err)
		}
		types = append(types, b.Type)
	}
	return types
}

func lastText(t *testing.T, m journal.Message) string {
	t.Helper()
	var b struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(m.Content[len(m.Content)-1], &b); err != nil {
		t.Fatal(err)
	}
	return b.Text
}

func plan(id, text string) json.RawMessage {
	return anthropictest.ToolUse(id, "plan", map[string]any{"items": []any{map[string]any{"text": text}}})
}

func TestRefusalIsRecordedAndStopsTheRun(t *testing.T) {
	w := newWorld(t)
	o := w.turn(anthropictest.Reply("refusal", anthropictest.Text("I can't"), plan("tu1", "half a call")))
	o.assertFailed(t, "refused")
	if o.run.Steps != 1 || o.usage != 1 {
		t.Errorf("steps %d, usage rows %d: the refused call must be metered", o.run.Steps, o.usage)
	}
	m := o.last(t)
	if m.Role != journal.Assistant || len(m.Content) != 1 || !strings.Contains(lastText(t, m), "declined") {
		t.Errorf("thread ends with %s %v", m.Role, blockTypes(t, m))
	}
}

func TestMaxTokensKeepsTextDropsTheHalfWrittenToolCallAndFails(t *testing.T) {
	w := newWorld(t)
	o := w.turn(anthropictest.Reply("max_tokens", anthropictest.Text("partial answer"), plan("tu1", "cut")))
	o.assertFailed(t, "max_tokens")
	m := o.last(t)
	if got := blockTypes(t, m); len(got) != 2 || got[0] != "text" || got[1] != "text" || !strings.Contains(lastText(t, m), "cut off") {
		t.Errorf("blocks %v", got)
	}
	if o.usage != 1 {
		t.Errorf("usage rows %d", o.usage)
	}
}

func TestContextWindowStopIsTreatedAsCutOff(t *testing.T) {
	w := newWorld(t)
	o := w.turn(anthropictest.Reply("model_context_window_exceeded", anthropictest.Text("so far")))
	o.assertFailed(t, "model_context_window_exceeded")
}

func TestEndTurnWithoutTextLeavesANotice(t *testing.T) {
	w := newWorld(t)
	o := w.turn(anthropictest.Reply("end_turn", anthropictest.Thinking("hm", "sig")))
	if o.run.State != journal.Done {
		t.Fatalf("run %s %v", o.run.State, o.run.Error)
	}
	m := o.last(t)
	if got := blockTypes(t, m); len(got) != 2 || got[0] != "thinking" || !strings.Contains(lastText(t, m), "without a reply") {
		t.Errorf("blocks %v", got)
	}
}

func TestEmptyPauseTurnFailsInsteadOfJournalingAnEmptyTurn(t *testing.T) {
	w := newWorld(t)
	o := w.turn(anthropictest.Reply("pause_turn"))
	o.assertFailed(t, "pause_turn")
	if m := o.last(t); len(m.Content) != 1 || !strings.Contains(lastText(t, m), "paused") {
		t.Errorf("blocks %v", blockTypes(t, m))
	}
}

func TestPauseTurnWithContentContinues(t *testing.T) {
	w := newWorld(t)
	o := w.turn(
		anthropictest.Reply("pause_turn", anthropictest.WebSearch("ws1", "q", "https://example.com/a")...),
		anthropictest.Reply("end_turn", anthropictest.Text("found it")),
	)
	if o.run.State != journal.Done || o.run.Steps != 2 {
		t.Fatalf("run %s steps %d %v", o.run.State, o.run.Steps, o.run.Error)
	}
}

func TestRepeatedIdenticalToolCallStopsAtTheThreshold(t *testing.T) {
	w := newWorld(t)
	o := w.turn(
		anthropictest.Reply("tool_use", plan("a", "same")),
		anthropictest.Reply("tool_use", plan("b", "same")),
		anthropictest.Reply("tool_use", plan("c", "same")),
	)
	o.assertFailed(t, "repeated 3 times")
	if o.run.Steps != 3 {
		t.Errorf("steps %d, want 3", o.run.Steps)
	}
	m := o.last(t)
	if m.Role != journal.User || blockTypes(t, m)[0] != "tool_result" {
		t.Errorf("thread must end on the last call's result, got %s %v", m.Role, blockTypes(t, m))
	}
}

func TestVaryingToolCallsDoNotTripTheDoomLoop(t *testing.T) {
	w := newWorld(t)
	o := w.turn(
		anthropictest.Reply("tool_use", plan("a", "same")),
		anthropictest.Reply("tool_use", plan("b", "same")),
		anthropictest.Reply("tool_use", plan("c", "other")),
		anthropictest.Reply("tool_use", plan("d", "other")),
		anthropictest.Reply("end_turn", anthropictest.Text("done")),
	)
	if o.run.State != journal.Done {
		t.Fatalf("run %s %v", o.run.State, o.run.Error)
	}
}

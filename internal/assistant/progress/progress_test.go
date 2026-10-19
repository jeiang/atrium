package progress_test

import (
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/anthropic"
	"github.com/jeiang/atrium/internal/assistant/journal"
	"github.com/jeiang/atrium/internal/assistant/progress"
	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/db/dbtest"
	"github.com/jeiang/atrium/internal/py"
)

type clock struct{ t float64 }

func (c *clock) now() float64 { return c.t }

func world(t *testing.T) (*db.DB, int64, int64) {
	t.Helper()
	d := dbtest.Schema(t)
	thread, err := threads.Create(t.Context(), d.Write, 1, threads.NewThread{Kind: threads.Chat})
	if err != nil {
		t.Fatal(err)
	}
	owner := "w"
	run, err := journal.StartRun(t.Context(), d.Write, thread, "chat", "m", "low", 1, nil, &owner)
	if err != nil {
		t.Fatal(err)
	}
	return d, thread, run
}

func since(t *testing.T, d *db.DB, run, after int64) []progress.Event {
	t.Helper()
	events, err := progress.Since(t.Context(), d.Read, run, after)
	if err != nil {
		t.Fatal(err)
	}
	return events
}

func TestEmitAndSince(t *testing.T) {
	d, thread, run := world(t)
	ctx := t.Context()
	owner := "w"
	child, _ := threads.Create(ctx, d.Write, 1, threads.NewThread{Kind: threads.Subagent, ParentID: &thread})
	childRun, err := journal.StartRun(ctx, d.Write, child, "sub", "m", "low", 1, &run, &owner)
	if err != nil {
		t.Fatal(err)
	}
	for i, c := range []struct {
		run  int64
		kind string
		data py.Object
	}{
		{run, progress.RunStart, py.Object{{Key: "kind", Value: "chat"}}},
		{childRun, progress.Text, py.Object{{Key: "text", Value: "é\u2028"}}},
		{run, progress.ToolDone, py.Object{{Key: "id", Value: "t"}, {Key: "ok", Value: true}, {Key: "preview", Value: "3 results"}}},
	} {
		if err := progress.Emit(ctx, d.Write, c.run, c.kind, c.data, float64(i)); err != nil {
			t.Fatal(err)
		}
	}
	events := since(t, d, run, 0)
	if len(events) != 3 || events[1].RunID != childRun || events[1].Kind != progress.Text {
		t.Fatalf("events %+v", events)
	}
	if string(events[0].Data) != `{"kind": "chat"}` || string(events[1].Data) != "{\"text\": \"é\u2028\"}" || string(events[2].Data) != `{"id": "t", "ok": true, "preview": "3 results"}` {
		t.Errorf("data %s | %s | %s", events[0].Data, events[1].Data, events[2].Data)
	}
	if got := since(t, d, run, events[1].ID); len(got) != 1 || got[0].ID != events[2].ID {
		t.Errorf("after the second event: %+v", got)
	}
	if got := since(t, d, childRun, 0); len(got) != 1 || got[0].RunID != childRun {
		t.Errorf("the child's own view: %+v", got)
	}
	var data map[string]any
	if err := json.Unmarshal(events[2].Data, &data); err != nil || data["ok"] != true {
		t.Errorf("data does not decode: %v", err)
	}
	if err := progress.ClearThread(ctx, d.Write, thread); err != nil {
		t.Fatal(err)
	}
	if got := since(t, d, run, 0); len(got) != 0 || got == nil {
		t.Errorf("after ClearThread: %+v", got)
	}
}

func TestWriterBuffersDeltasAndFlushesOnKindChange(t *testing.T) {
	d, _, run := world(t)
	ctx := t.Context()
	c := &clock{t: 10}
	w := progress.NewWriter(d.Write, run, c.now)
	step := func(kind, text string, advance float64) {
		t.Helper()
		c.t += advance
		if err := w.Delta(ctx, kind, text); err != nil {
			t.Fatal(err)
		}
	}
	step(progress.Thinking, "a", 0)
	step(progress.Thinking, "b", 0.05)
	step(progress.Thinking, "c", 0.05)
	step(progress.Text, "x", 0.01)
	step(progress.Text, "y", 0.3)
	step(progress.Text, "z", 0.01)
	if err := w.Event(ctx, progress.ToolStart, py.Object{{Key: "id", Value: "t1"}, {Key: "name", Value: "web_search"}, {Key: "args", Value: "{}"}}); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range since(t, d, run, 0) {
		got = append(got, e.Kind+" "+string(e.Data))
	}
	want := []string{`thinking {"text": "abc"}`, `text {"text": "xy"}`, `text {"text": "z"}`, `tool_start {"id": "t1", "name": "web_search", "args": "{}"}`}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("events\n got %q\nwant %q", got, want)
	}
	var ts []float64
	rows, err := d.Read.QueryContext(ctx, "SELECT ts FROM run_events WHERE run_id = ? ORDER BY id", run)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var v float64
		if err := rows.Scan(&v); err != nil {
			t.Fatal(err)
		}
		ts = append(ts, v)
	}
	_ = rows.Close()
	wantTS := []float64{10.11, 10.41, 10.42, 10.42}
	if len(ts) != len(wantTS) {
		t.Fatalf("timestamps %v", ts)
	}
	for i := range ts {
		if math.Abs(ts[i]-wantTS[i]) > 1e-9 {
			t.Errorf("event %d at %v, want %v", i, ts[i], wantTS[i])
		}
	}
}

func TestCheckStop(t *testing.T) {
	d, _, run := world(t)
	ctx := t.Context()
	c := &clock{t: 100}
	w := progress.NewWriter(d.Write, run, c.now)
	if err := w.CheckStop(ctx, false); err != nil {
		t.Fatal(err)
	}
	if ok, err := journal.RequestStop(ctx, d.Write, run); err != nil || !ok {
		t.Fatal(err)
	}
	if err := w.CheckStop(ctx, false); err != nil {
		t.Errorf("checked again within a second: %v", err)
	}
	c.t += 1.1
	if err := w.CheckStop(ctx, false); !errors.Is(err, progress.ErrStopped) {
		t.Errorf("CheckStop after a second = %v", err)
	}
	if err := w.CheckStop(ctx, true); !errors.Is(err, progress.ErrStopped) {
		t.Errorf("forced CheckStop = %v", err)
	}
	if err := progress.NewWriter(d.Write, 999, c.now).CheckStop(ctx, true); err != nil {
		t.Errorf("an unknown run: %v", err)
	}
	if progress.ErrStopped.Error() != "stopped by the operator" {
		t.Errorf("message %q", progress.ErrStopped)
	}
}

func TestObserve(t *testing.T) {
	d, _, run := world(t)
	ctx := t.Context()
	c := &clock{t: 50}
	w := progress.NewWriter(d.Write, run, c.now)
	input := json.RawMessage(`{"query": "weather in Port of Spain", "n": 1}`)
	for _, ev := range []anthropic.Event{
		{Kind: anthropic.EventThinking, Text: "hmm"},
		{Kind: anthropic.EventText, Text: "Looking"},
		{Kind: anthropic.EventToolStart, ID: "srv1", Name: "web_search", Input: input},
		{Kind: anthropic.EventToolDone, ID: "srv1", OK: true, Preview: "5 results"},
		{Kind: anthropic.EventToolDone, ID: "srv2", OK: false, Preview: "0 results"},
		{},
	} {
		c.t += 0.01
		if err := w.Observe(ctx, ev); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range since(t, d, run, 0) {
		got = append(got, e.Kind+" "+string(e.Data))
	}
	want := []string{
		`thinking {"text": "hmm"}`, `text {"text": "Looking"}`,
		`tool_start {"id": "srv1", "name": "web_search", "args": "{\"query\": \"weather in Port of Spain\", \"n\": 1}"}`,
		`tool_done {"id": "srv1", "ok": true, "preview": "5 results"}`, `tool_done {"id": "srv2", "ok": false, "preview": "0 results"}`,
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("events\n got %q\nwant %q", got, want)
	}
	if _, err := journal.RequestStop(ctx, d.Write, run); err != nil {
		t.Fatal(err)
	}
	c.t += 2
	if err := w.Observe(ctx, anthropic.Event{Kind: anthropic.EventText, Text: "more"}); !errors.Is(err, progress.ErrStopped) {
		t.Errorf("Observe after a stop request = %v", err)
	}
}

func TestShortArgs(t *testing.T) {
	short, err := progress.ShortArgs(json.RawMessage(`{"a":"é","b":[1,2]}`))
	if err != nil || short != `{"a": "é", "b": [1, 2]}` {
		t.Errorf("ShortArgs = %q, %v", short, err)
	}
	long, err := progress.ShortArgs(json.RawMessage(`{"q":"` + strings.Repeat("é", 200) + `"}`))
	if err != nil || len([]rune(long)) != progress.ArgsChars || !strings.HasSuffix(long, "…") {
		t.Errorf("long ShortArgs = %q (%d runes), %v", long, len([]rune(long)), err)
	}
	if _, err := progress.ShortArgs(json.RawMessage(`{`)); err == nil {
		t.Error("broken JSON")
	}
}

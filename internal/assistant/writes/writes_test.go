package writes_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/assistant/writes"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/db/dbtest"
)

func world(t *testing.T) (*db.DB, int64, int64) {
	t.Helper()
	d := dbtest.Schema(t)
	thread, err := threads.Create(t.Context(), d.Write, 0, threads.NewThread{Kind: threads.Chat})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.ExecContext(t.Context(), "INSERT INTO runs (id, thread_id, kind, model, effort, state, started_at) VALUES (1, ?, 'chat', 'm', 'low', 'running', 0)", thread); err != nil {
		t.Fatal(err)
	}
	return d, thread, 1
}

func wantUndoError(t *testing.T, err error, msg string) {
	t.Helper()
	var ue writes.UndoError
	if !errors.As(err, &ue) || err.Error() != msg {
		t.Fatalf("error %v, want undo error %q", err, msg)
	}
}

func TestRecordDumpsSortedJSON(t *testing.T) {
	d, thread, run := world(t)
	ctx := t.Context()
	id, err := writes.Record(ctx, d.Write, writes.New{
		TS: 12, ThreadID: &thread, RunID: &run, Tool: "profile_set", Target: "profile:preferred_airline",
		Before: map[string]any{"value": nil}, After: map[string]any{"value": "Island Air é"},
		Undo: map[string]any{"op": "profile_restore", "key": "preferred_airline", "previous": nil, "applied": "Island Air é"},
	})
	if err != nil {
		t.Fatal(err)
	}
	row, err := writes.Get(ctx, d.Read, id)
	if err != nil || row == nil {
		t.Fatalf("Get = %v, %v", row, err)
	}
	if *row.BeforeJSON != `{"value": null}` || *row.AfterJSON != `{"value": "Island Air \u00e9"}` ||
		*row.UndoJSON != `{"applied": "Island Air \u00e9", "key": "preferred_airline", "op": "profile_restore", "previous": null}` ||
		row.TS != 12 || *row.ThreadID != thread || row.Tool != "profile_set" || row.UndoneAt != nil {
		t.Errorf("row %+v", row)
	}
	loose, err := writes.Record(ctx, d.Write, writes.New{TS: 1, Tool: "calendar_add", Target: "e1"})
	if err != nil {
		t.Fatal(err)
	}
	if row, _ := writes.Get(ctx, d.Read, loose); row.ThreadID != nil || row.RunID != nil || row.BeforeJSON != nil || row.AfterJSON != nil || row.UndoJSON != nil {
		t.Errorf("a write made outside a run: %+v", row)
	}
	if got, err := writes.Get(ctx, d.Read, 999); err != nil || got != nil {
		t.Errorf("Get(unknown) = %v, %v", got, err)
	}
}

func TestUndo(t *testing.T) {
	d, thread, run := world(t)
	ctx := t.Context()
	reg := writes.NewRegistry()
	var calls []map[string]any
	reg.Register("restore", func(ctx context.Context, _ db.Querier, info map[string]any) (any, error) {
		calls = append(calls, info)
		if info["fail"] == true {
			return nil, writes.UndoError("changed since")
		}
		return "restored", nil
	})
	record := func(undo any) int64 {
		t.Helper()
		id, err := writes.Record(ctx, d.Write, writes.New{TS: 1, ThreadID: &thread, RunID: &run, Tool: "t", Target: "x", Undo: undo})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	now := func() float64 { return 77 }
	good := record(map[string]any{"op": "restore", "key": "k"})
	got, err := reg.Undo(ctx, d, good, now)
	if err != nil || got != "restored" || len(calls) != 1 || calls[0]["key"] != "k" {
		t.Fatalf("Undo = %v, %v (calls %v)", got, err, calls)
	}
	if row, _ := writes.Get(ctx, d.Read, good); row.UndoneAt == nil || *row.UndoneAt != 77 {
		t.Errorf("undone_at %+v", row)
	}
	_, err = reg.Undo(ctx, d, good, now)
	wantUndoError(t, err, "already undone")
	_, err = reg.Undo(ctx, d, 999, now)
	wantUndoError(t, err, "no write 999")
	for _, undo := range []any{nil, map[string]any{}, []any{}} {
		_, err = reg.Undo(ctx, d, record(undo), now)
		wantUndoError(t, err, "this write has no undo")
	}
	_, err = reg.Undo(ctx, d, record(map[string]any{"op": "mystery"}), now)
	wantUndoError(t, err, "unknown undo kind 'mystery'")
	_, err = reg.Undo(ctx, d, record(map[string]any{"key": 1}), now)
	wantUndoError(t, err, "unknown undo kind None")
	refused := record(map[string]any{"op": "restore", "fail": true})
	_, err = reg.Undo(ctx, d, refused, now)
	wantUndoError(t, err, "changed since")
	if row, _ := writes.Get(ctx, d.Read, refused); row.UndoneAt != nil {
		t.Errorf("a refused undo was marked done: %+v", row)
	}
}

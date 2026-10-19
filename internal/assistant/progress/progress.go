// Package progress is agent/progress.py: the live journal of a run, one run_events row per step the page
// replays, and the Writer that buffers streamed deltas into few rows and notices a stop request.
package progress

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/jeiang/atrium/internal/db"

	"github.com/jeiang/atrium/internal/anthropic"
	"github.com/jeiang/atrium/internal/assistant/store"
	"github.com/jeiang/atrium/internal/py"
)

// Event kinds.
const (
	RunStart  = "run_start"
	Thinking  = "thinking"
	Text      = "text"
	ToolStart = "tool_start"
	ToolDone  = "tool_done"
	User      = "user"
	Plan      = "plan"
	Files     = "files"
	RunDone   = "run_done"
)

const (
	FlushSeconds = 0.2
	CheckSeconds = 1.0
	ArgsChars    = 120
)

// ErrStopped is the error a Writer raises when the operator asked the run to stop.
var ErrStopped = errors.New("stopped by the operator")

var dataOpts = py.JSONOpts{NoEnsureASCII: true}

// ShortArgs is short_args for a tool's input: its JSON, cut to ArgsChars with an ellipsis.
func ShortArgs(args json.RawMessage) (string, error) {
	text, err := py.JSONRedump(args, dataOpts)
	if err != nil {
		return "", err
	}
	if utf8.RuneCountInString(text) <= ArgsChars {
		return text, nil
	}
	return py.Clip(text, ArgsChars-1) + "…", nil
}

// Emit is emit: it journals one event of the run. data is dumped with its keys in the order given.
func Emit(ctx context.Context, q db.Querier, runID int64, kind string, data py.Dict, now float64) error {
	_, err := q.ExecContext(ctx, "INSERT INTO run_events (run_id, ts, kind, data_json) VALUES (?, ?, ?, ?)", runID, now, kind, py.JSONDumps(data, dataOpts))
	return err
}

// Event is a journaled run event of a run or of one of its subagent runs.
type Event struct {
	ID    int64           `json:"id"`
	RunID int64           `json:"run_id"`
	Kind  string          `json:"kind"`
	Data  json.RawMessage `json:"data"`
}

// Since is since: the events of the run and of its subagent runs, in id order, after afterID.
func Since(ctx context.Context, q db.Querier, runID, afterID int64) ([]Event, error) {
	rows, err := q.QueryContext(ctx,
		"SELECT e.id, e.run_id, e.kind, e.data_json FROM run_events e WHERE e.id > ? "+
			"AND (e.run_id = ? OR e.run_id IN (SELECT id FROM runs WHERE parent_run_id = ?)) ORDER BY e.id", afterID, runID, runID)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (Event, error) {
		var e Event
		var data string
		if err := r.Scan(&e.ID, &e.RunID, &e.Kind, &data); err != nil {
			return Event{}, err
		}
		e.Data = json.RawMessage(data)
		return e, nil
	})
}

// ClearThread is clear_thread: it drops the progress of the thread's earlier runs (and their subagent runs).
// Called when a new run starts, so a finished run's events stay readable until then: a page that connects
// late still replays them.
func ClearThread(ctx context.Context, q db.Querier, threadID int64) error {
	_, err := q.ExecContext(ctx,
		"DELETE FROM run_events WHERE run_id IN (SELECT id FROM runs WHERE thread_id = ? OR parent_run_id IN (SELECT id FROM runs WHERE thread_id = ?))",
		threadID, threadID)
	return err
}

// Writer journals a run's live progress. Streamed thinking and answer deltas are buffered and written as one
// row per FlushSeconds, so a long answer is a few dozen rows rather than one per token.
type Writer struct {
	q         db.Querier
	runID     int64
	now       func() float64
	kind      string
	buffer    []string
	flushedAt float64
	checkedAt float64
}

// NewWriter is Writer(conn, run_id, now).
func NewWriter(q db.Querier, runID int64, now func() float64) *Writer {
	return &Writer{q: q, runID: runID, now: now}
}

// Event is Writer.event: it flushes the buffered deltas and journals the event.
func (w *Writer) Event(ctx context.Context, kind string, data py.Dict) error {
	if err := w.Flush(ctx); err != nil {
		return err
	}
	return Emit(ctx, w.q, w.runID, kind, data, w.now())
}

// Delta is Writer.delta.
func (w *Writer) Delta(ctx context.Context, kind, text string) error {
	if kind != w.kind {
		if err := w.Flush(ctx); err != nil {
			return err
		}
		w.kind = kind
	}
	w.buffer = append(w.buffer, text)
	if w.now()-w.flushedAt >= FlushSeconds {
		return w.Flush(ctx)
	}
	return nil
}

// Flush is Writer.flush.
func (w *Writer) Flush(ctx context.Context) error {
	text := strings.Join(w.buffer, "")
	w.buffer = w.buffer[:0]
	w.flushedAt = w.now()
	if text == "" {
		return nil
	}
	return Emit(ctx, w.q, w.runID, w.kind, py.Dict{{Key: "text", Value: text}}, w.flushedAt)
}

// CheckStop is check_stop: ErrStopped when the operator asked the run to stop. Without force it asks the
// database at most once per CheckSeconds.
func (w *Writer) CheckStop(ctx context.Context, force bool) error {
	if !force && w.now()-w.checkedAt < CheckSeconds {
		return nil
	}
	w.checkedAt = w.now()
	var requested store.Flag
	err := w.q.QueryRowContext(ctx, "SELECT stop_requested FROM runs WHERE id = ?", w.runID).Scan(&requested)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if requested {
		return ErrStopped
	}
	return nil
}

// Observe is observe for one stream event: thinking and text deltas, and the server-side web_search tool as
// it runs.
func (w *Writer) Observe(ctx context.Context, ev anthropic.Event) error {
	var err error
	switch ev.Kind {
	case anthropic.EventThinking:
		err = w.Delta(ctx, Thinking, ev.Text)
	case anthropic.EventText:
		err = w.Delta(ctx, Text, ev.Text)
	case anthropic.EventToolStart:
		var args string
		if args, err = ShortArgs(ev.Input); err == nil {
			err = w.Event(ctx, ToolStart, py.Dict{{Key: "id", Value: ev.ID}, {Key: "name", Value: ev.Name}, {Key: "args", Value: args}})
		}
	case anthropic.EventToolDone:
		err = w.Event(ctx, ToolDone, py.Dict{{Key: "id", Value: ev.ID}, {Key: "ok", Value: ev.OK}, {Key: "preview", Value: ev.Preview}})
	}
	if err != nil {
		return err
	}
	return w.CheckStop(ctx, false)
}

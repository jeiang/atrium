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
	PreviewChars = 240
)

var ErrStopped = errors.New("stopped by the operator")

var dataOpts = py.JSONOpts{NoEnsureASCII: true}

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

func PreviewOf(content any) string {
	text, ok := content.(string)
	if !ok {
		text = py.JSONDumps(content, dataOpts)
	}
	if utf8.RuneCountInString(text) <= PreviewChars {
		return text
	}
	return py.Clip(text, PreviewChars-1) + "…"
}

func Emit(ctx context.Context, q db.Querier, runID int64, kind string, data py.Dict, now float64) error {
	_, err := q.ExecContext(ctx, "INSERT INTO run_events (run_id, ts, kind, data_json) VALUES (?, ?, ?, ?)", runID, now, kind, py.JSONDumps(data, dataOpts))
	return err
}

type Event struct {
	ID    int64           `json:"id"`
	RunID int64           `json:"run_id"`
	Kind  string          `json:"kind"`
	Data  json.RawMessage `json:"data"`
}

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

func ClearThread(ctx context.Context, q db.Querier, threadID int64) error {
	_, err := q.ExecContext(ctx,
		"DELETE FROM run_events WHERE run_id IN (SELECT id FROM runs WHERE thread_id = ? OR parent_run_id IN (SELECT id FROM runs WHERE thread_id = ?))",
		threadID, threadID)
	return err
}

type Writer struct {
	q         db.Querier
	runID     int64
	now       func() float64
	kind      string
	buffer    []string
	flushedAt float64
	checkedAt float64
}

func NewWriter(q db.Querier, runID int64, now func() float64) *Writer {
	return &Writer{q: q, runID: runID, now: now}
}

func (w *Writer) Event(ctx context.Context, kind string, data py.Dict) error {
	if err := w.Flush(ctx); err != nil {
		return err
	}
	return Emit(ctx, w.q, w.runID, kind, data, w.now())
}

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

func (w *Writer) Flush(ctx context.Context) error {
	text := strings.Join(w.buffer, "")
	w.buffer = w.buffer[:0]
	w.flushedAt = w.now()
	if text == "" {
		return nil
	}
	return Emit(ctx, w.q, w.runID, w.kind, py.Dict{{Key: "text", Value: text}}, w.flushedAt)
}

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

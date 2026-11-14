package progress

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/jsonx"
	"github.com/jeiang/atrium/internal/textx"

	"github.com/jeiang/atrium/internal/anthropic"
	"github.com/jeiang/atrium/internal/db/store"
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

var (
	ErrStopped  = errors.New("stopped by the operator")
	ErrShutdown = errors.New("serve is shutting down; the run resumes on restart")
)

var dataOpts = jsonx.Opts{NoEnsureASCII: true}

func ShortArgs(args json.RawMessage) (string, error) {
	text, err := jsonx.Redump(args, dataOpts)
	if err != nil {
		return "", err
	}
	if utf8.RuneCountInString(text) <= ArgsChars {
		return text, nil
	}
	return textx.Clip(text, ArgsChars-1) + "…", nil
}

func PreviewOf(content any) string {
	text, ok := content.(string)
	if !ok {
		text = jsonx.Dumps(content, dataOpts)
	}
	if utf8.RuneCountInString(text) <= PreviewChars {
		return text
	}
	return textx.Clip(text, PreviewChars-1) + "…"
}

type Event struct {
	ID    int64           `json:"id"`
	RunID int64           `json:"run_id"`
	Kind  string          `json:"kind"`
	Data  json.RawMessage `json:"data"`
}

type Writer struct {
	q         db.Querier
	runID     int64
	now       func() float64
	kind      string
	buffer    []string
	flushedAt float64
	checkedAt float64
	stop      <-chan struct{}
}

func NewWriter(q db.Querier, runID int64, now func() float64, stop <-chan struct{}) *Writer {
	return &Writer{q: q, runID: runID, now: now, stop: stop}
}

func (w *Writer) Event(ctx context.Context, kind string, data map[string]any) error {
	if err := w.Flush(ctx); err != nil {
		return err
	}
	return Write(ctx, w.q, func(tx *sql.Tx) error {
		return Append(ctx, tx, w.runID, Payload{Kind: kind, Data: data, Time: w.now()})
	})
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
	return Write(ctx, w.q, func(tx *sql.Tx) error {
		return Append(ctx, tx, w.runID, Payload{Kind: w.kind, Data: map[string]any{"text": text}, Time: w.flushedAt})
	})
}

func (w *Writer) CheckStop(ctx context.Context, force bool) error {
	if force {
		select {
		case <-w.stop:
			return ErrShutdown
		default:
		}
	} else if w.now()-w.checkedAt < CheckSeconds {
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
	switch e := ev.(type) {
	case anthropic.ThinkingEvent:
		err = w.Delta(ctx, Thinking, e.Text)
	case anthropic.TextEvent:
		err = w.Delta(ctx, Text, e.Text)
	case anthropic.ToolStartEvent:
		var args string
		if args, err = ShortArgs(e.Input); err == nil {
			err = w.Event(ctx, ToolStart, map[string]any{"id": e.ID, "name": e.Name, "args": args})
		}
	case anthropic.ToolDoneEvent:
		err = w.Event(ctx, ToolDone, map[string]any{"id": e.ID, "ok": e.OK, "preview": e.Preview})
	}
	if err != nil {
		return err
	}
	return w.CheckStop(ctx, false)
}

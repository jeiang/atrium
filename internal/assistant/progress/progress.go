package progress

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/jeiang/atrium/internal/jsonx"
	"github.com/jeiang/atrium/internal/textx"

	"github.com/jeiang/atrium/internal/anthropic"
	"github.com/jeiang/atrium/internal/db/store"
)

const (
	RunStart     = "run_start"
	Thinking     = "thinking"
	Text         = "text"
	ToolStart    = "tool_start"
	ToolProgress = "tool_progress"
	ToolDone     = "tool_done"
	User         = "user"
	Plan         = "plan"
	Files        = "files"
	RunDone      = "run_done"
)

const SearchWebTool = "search_web"

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

type Result struct {
	URL   string
	Title string
}

func NewResult(url, title string) (Result, bool) {
	if url == "" {
		return Result{}, false
	}
	if title == "" {
		title = url
	}
	return Result{URL: url, Title: title}, true
}

func ResultsOf(list []any) []Result {
	var out []Result
	for _, raw := range list {
		entry, _ := raw.(map[string]any)
		url, _ := entry["url"].(string)
		title, _ := entry["title"].(string)
		if r, ok := NewResult(url, title); ok {
			out = append(out, r)
		}
	}
	return out
}

func ToolResults(name string, content any) []Result {
	text, ok := content.(string)
	if name != SearchWebTool || !ok {
		return nil
	}
	decoded, err := jsonx.Loads([]byte(text))
	if err != nil {
		return nil
	}
	fields, _ := decoded.(map[string]any)
	list, _ := fields["sources"].([]any)
	return ResultsOf(list)
}

func ResultsData(results []Result) []any {
	out := make([]any, len(results))
	for i, r := range results {
		out[i] = map[string]any{"url": r.URL, "title": r.Title}
	}
	return out
}

type Event struct {
	ID    int64           `json:"id"`
	RunID int64           `json:"run_id"`
	Kind  string          `json:"kind"`
	Data  json.RawMessage `json:"data"`
}

type parentTool struct {
	runID int64
	id    string
}

type parentToolKey struct{}

func WithTool(ctx context.Context, runID int64, id string) context.Context {
	return context.WithValue(ctx, parentToolKey{}, parentTool{runID: runID, id: id})
}

type Writer struct {
	q         *sql.DB
	runID     int64
	now       func() float64
	kind      string
	buffer    []string
	flushedAt float64
	checkedAt float64
	stop      <-chan struct{}
	parent    parentTool
}

func NewWriter(ctx context.Context, q *sql.DB, runID int64, now func() float64, stop <-chan struct{}) *Writer {
	parent, _ := ctx.Value(parentToolKey{}).(parentTool)
	return &Writer{q: q, runID: runID, now: now, stop: stop, parent: parent}
}

func (w *Writer) Event(ctx context.Context, kind string, data map[string]any) error {
	if err := w.Flush(ctx); err != nil {
		return err
	}
	if err := Write(ctx, w.q, func(tx *sql.Tx) error {
		return Append(ctx, tx, w.runID, Payload{Kind: kind, Data: data, Time: w.now()})
	}); err != nil {
		return err
	}
	if kind != ToolStart || w.parent.id == "" {
		return nil
	}
	name, _ := data["name"].(string)
	args, _ := data["args"].(string)
	return Publish(ctx, w.q, w.parent.runID, ToolProgress, map[string]any{"id": w.parent.id, "text": strings.TrimSpace(name + " " + args)})
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
		results := []Result{}
		for _, r := range e.Results {
			if found, ok := NewResult(r.URL, r.Title); ok {
				results = append(results, found)
			}
		}
		err = w.Event(ctx, ToolDone, map[string]any{"id": e.ID, "ok": e.OK, "preview": e.Preview, "results": ResultsData(results)})
	}
	if err != nil {
		return err
	}
	return w.CheckStop(ctx, false)
}

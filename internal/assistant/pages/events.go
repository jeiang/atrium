package pages

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/jeiang/atrium/internal/assistant/journal"
	"github.com/jeiang/atrium/internal/assistant/progress"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/web"
)

var log = logx.Logger("atrium.web.chat")

func sseFrame(e progress.Event) (string, error) {
	decoded, err := py.JSONLoads(e.Data)
	if err != nil {
		return "", err
	}
	data := py.Dict{{Key: "run_id", Value: e.RunID}}
	fields, _ := decoded.(py.Dict)
	for _, pair := range fields {
		if pair.Key == "run_id" {
			data[0].Value = pair.Value
			continue
		}
		data = append(data, pair)
	}
	return fmt.Sprintf("id: %d\nevent: %s\ndata: %s\n\n", e.ID, e.Kind, py.JSONDumps(data, py.JSONOpts{NoEnsureASCII: true})), nil
}

func (p *pages) chatEvents(w http.ResponseWriter, r *http.Request) {
	threadID, errs := pathInt(r, "thread_id")
	var runID int64
	if values := r.URL.Query()["run"]; len(values) == 0 {
		errs = append(errs, web.Missing("query", "run"))
	} else if n, ok := py.LaxInt64(values[len(values)-1]); ok {
		runID = n
	} else {
		errs = append(errs, web.BadInt("query", "run", values[len(values)-1]))
	}
	after := queryAfter(r, &errs)
	if len(errs) > 0 {
		web.Unprocessable(w, errs...)
		return
	}
	if header := r.Header.Get("Last-Event-ID"); header != "" {
		n, err := py.IntString(header)
		check(err)
		after = n.Int64()
	}
	if _, ok := p.loadRun(w, r, threadID, runID); !ok {
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher := http.NewResponseController(w)
	if err := p.stream(r, w, flusher, runID, after); err != nil && r.Context().Err() == nil {
		log.Error("event stream failed", logx.RunID, runID, logx.Err, err)
	}
}

func (p *pages) stream(r *http.Request, w io.Writer, flusher *http.ResponseController, runID, last int64) error {
	ctx := r.Context()
	quiet := time.Now()
	for {
		events, err := progress.Since(ctx, p.deps.DB.Read, runID, last)
		if err != nil {
			return err
		}
		for _, e := range events {
			last = e.ID
			frame, err := sseFrame(e)
			if err != nil {
				return err
			}
			if _, err := io.WriteString(w, frame); err != nil {
				return nil
			}
		}
		if len(events) > 0 {
			quiet = time.Now()
		}
		found, err := journal.GetRun(ctx, p.deps.DB.Read, runID)
		if err != nil {
			return err
		}
		if found == nil || found.State != journal.Running {
			if found != nil {
				if _, err = p.drainRunDone(ctx, w, runID, last); err != nil {
					return err
				}
			}
			var state any
			if found != nil {
				state = found.State
			}
			_, _ = io.WriteString(w, "event: done\ndata: "+py.JSONDumps(py.Dict{{Key: "state", Value: state}}, py.JSONOpts{NoEnsureASCII: true})+"\n\n")
			_ = flusher.Flush()
			return nil
		}
		if time.Since(quiet) >= p.keepalive {
			quiet = time.Now()
			if _, err := io.WriteString(w, ": keepalive\n\n"); err != nil {
				return nil
			}
		}
		if err := flusher.Flush(); err != nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-p.deps.Stopping:
			return nil
		case <-time.After(p.poll):
		}
	}
}

const runDoneGrace = 3 * time.Second

func (p *pages) drainRunDone(ctx context.Context, w io.Writer, runID, last int64) (int64, error) {
	deadline := time.Now().Add(runDoneGrace)
	for {
		var written int
		if err := p.deps.DB.Read.QueryRowContext(ctx, "SELECT COUNT(*) FROM run_events WHERE run_id = ? AND kind = ?", runID, progress.RunDone).Scan(&written); err != nil {
			return last, err
		}
		events, err := progress.Since(ctx, p.deps.DB.Read, runID, last)
		if err != nil {
			return last, err
		}
		for _, e := range events {
			last = e.ID
			frame, err := sseFrame(e)
			if err != nil {
				return last, err
			}
			if _, err := io.WriteString(w, frame); err != nil {
				return last, nil
			}
		}
		if written > 0 || time.Now().After(deadline) {
			return last, nil
		}
		select {
		case <-ctx.Done():
			return last, nil
		case <-p.deps.Stopping:
			return last, nil
		case <-time.After(p.poll):
		}
	}
}

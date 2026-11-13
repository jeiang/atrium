package pages

import (
	"context"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"time"

	asstapp "github.com/jeiang/atrium/internal/assistant/application"
	"github.com/jeiang/atrium/internal/assistant/journal"
	"github.com/jeiang/atrium/internal/assistant/progress"
	"github.com/jeiang/atrium/internal/jsonx"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/numx"
	"github.com/jeiang/atrium/internal/web"
)

var log = logx.Logger("atrium.web.chat")

func sseFrame(e progress.Event) (string, error) {
	decoded, err := jsonx.Loads(e.Data)
	if err != nil {
		return "", err
	}
	data := map[string]any{"run_id": e.RunID}
	fields, _ := decoded.(map[string]any)
	for k, v := range fields {
		if k == "run_id" {
			data["run_id"] = v
			continue
		}
		data[k] = v
	}
	return fmt.Sprintf("id: %d\nevent: %s\ndata: %s\n\n", e.ID, e.Kind, jsonx.Dumps(data, jsonx.Opts{NoEnsureASCII: true})), nil
}

func (p *pages) chatEvents(w http.ResponseWriter, r *http.Request) {
	threadID, errs := pathInt(r, "thread_id")
	var runID int64
	if values := r.URL.Query()["run"]; len(values) == 0 {
		errs = append(errs, web.Missing("query", "run"))
	} else if n, ok := numx.LaxInt64(values[len(values)-1]); ok {
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
		n, ok := new(big.Int).SetString(header, 10)
		if !ok {
			p.fail(w, r, http.StatusBadRequest, "invalid Last-Event-ID")
			return
		}
		after = n.Int64()
	}
	read, err := p.assistant().RunEvents(r.Context(), asstapp.RunInput{ThreadID: threadID, RunID: runID, After: after})
	if err != nil {
		p.readFailed(w, r, err)
		return
	}
	p.serveStream(w, r, read.Data)
}

func (p *pages) serveStream(w http.ResponseWriter, r *http.Request, events asstapp.EventStream) {
	runID, after := events.RunID, events.After
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
		batch, err := progress.Read(ctx, p.deps.DB.Read, runID, last, 100)
		if err != nil {
			return err
		}
		for _, e := range batch.Events {
			last = e.ID
			frame, err := sseFrame(e)
			if err != nil {
				return err
			}
			if _, err := io.WriteString(w, frame); err != nil {
				return nil
			}
		}
		if len(batch.Events) > 0 {
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
			_, _ = io.WriteString(w, "event: done\ndata: "+jsonx.Dumps(map[string]any{"state": state}, jsonx.Opts{NoEnsureASCII: true})+"\n\n")
			_ = flusher.Flush()
			return nil
		}
		if time.Since(quiet) >= streamKeepAlive {
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
		case <-time.After(streamPoll):
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
		batch, err := progress.Read(ctx, p.deps.DB.Read, runID, last, 100)
		if err != nil {
			return last, err
		}
		for _, e := range batch.Events {
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
		case <-time.After(streamPoll):
		}
	}
}

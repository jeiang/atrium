package pages

import (
	"context"
	"errors"
	"math/big"
	"net/http"

	"github.com/jeiang/atrium/internal/application"
	asstapp "github.com/jeiang/atrium/internal/assistant/application"
	"github.com/jeiang/atrium/internal/assistant/journal"
	"github.com/jeiang/atrium/internal/assistant/progress"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/numx"
	"github.com/jeiang/atrium/internal/web"
)

var log = logx.Logger("atrium.web.chat")

type streamRefusal func(status int, code, message string, runID int64)

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
	p.serveStream(w, r, threadID, runID, after, func(status int, _ string, message string, _ int64) {
		p.fail(w, r, status, message)
	})
}

func (p *pages) serveStream(w http.ResponseWriter, r *http.Request, threadID, runID, after int64, refuse streamRefusal) {
	if header := r.Header.Get("Last-Event-ID"); header != "" {
		n, ok := new(big.Int).SetString(header, 10)
		if !ok || !n.IsInt64() {
			refuse(http.StatusBadRequest, "bad_request", "Last-Event-ID must be an integer.", runID)
			return
		}
		after = n.Int64()
	}
	ctx := r.Context()
	read, err := p.assistant().RunEvents(ctx, asstapp.RunInput{ThreadID: threadID, RunID: runID, After: after})
	switch {
	case errors.Is(err, progress.ErrExpired):
		refuse(http.StatusGone, "stream_expired", "The stream no longer holds events after this cursor.", runID)
		return
	case errors.Is(err, progress.ErrFuture):
		refuse(http.StatusBadRequest, "bad_request", "The cursor is not in this stream.", runID)
		return
	case errorIs(err, application.NotFound):
		refuse(http.StatusNotFound, "not_found", "Unknown run.", runID)
		return
	}
	check(err)
	if read.Data.Finished {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	key, recheck := web.StreamCredential(ctx)
	release, err := progress.Acquire(key)
	if err != nil {
		refuse(http.StatusTooManyRequests, "rate_limited", "Too many open streams.", runID)
		return
	}
	defer release()
	live := progress.Live{Stopping: p.deps.Stopping, Check: recheck, State: func(ctx context.Context) (string, bool, error) {
		run, err := journal.GetRun(ctx, p.deps.DB.Read, runID)
		if err != nil || run == nil {
			return "", false, err
		}
		return run.State, run.State == journal.Running, nil
	}}
	stream := progress.Stream{RunID: runID, After: after}
	if err := progress.Serve(ctx, w, p.deps.DB.Read, stream, live); err != nil && ctx.Err() == nil {
		log.Error("event stream failed", logx.RunID, runID, logx.Err, err)
	}
}

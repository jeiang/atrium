package assistantmodule

import (
	"context"
	"errors"
	"math/big"
	"net/http"

	"github.com/jeiang/atrium/internal/application"
	asstapp "github.com/jeiang/atrium/internal/assistant/application"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/web"
)

var streamLog = logx.Logger("atrium.web.chat")

func runEvents(d *web.Deps, assistant func() *asstapp.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		threadID, ok := web.PathID(w, r, "thread_id")
		if !ok {
			return
		}
		runID, ok := web.PathID(w, r, "run_id")
		if !ok {
			return
		}
		name, raw := "Last-Event-ID", r.Header.Get("Last-Event-ID")
		if values := r.URL.Query()["after"]; raw == "" && len(values) > 0 {
			name, raw = "after", values[len(values)-1]
		}
		var after int64
		if raw != "" {
			n, ok := new(big.Int).SetString(raw, 10)
			if !ok || !n.IsInt64() {
				web.APIError(w, http.StatusBadRequest, "bad_request", name+" must be an integer.")
				return
			}
			after = n.Int64()
		}
		ctx := r.Context()
		read, err := assistant().RunEvents(ctx, asstapp.RunInput{ThreadID: threadID, RunID: runID, After: after})
		switch {
		case errors.Is(err, asstapp.ErrStreamExpired):
			web.APIErrorWith(w, http.StatusGone, "stream_expired", "The stream no longer holds events after this cursor.", map[string]any{"run_id": runID})
			return
		case errors.Is(err, asstapp.ErrStreamFuture):
			web.APIError(w, http.StatusBadRequest, "bad_request", "The cursor is not in this stream.")
			return
		case errorIs(err, application.NotFound):
			web.APIError(w, http.StatusNotFound, "not_found", "Unknown run.")
			return
		case err != nil:
			panic(err)
		}
		if read.Data.Finished {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		release, recheck, err := web.OpenStream(ctx, application.RunStream)
		switch {
		case errors.Is(err, web.ErrStreamLimit):
			web.APIError(w, http.StatusTooManyRequests, "rate_limited", "Too many open streams.")
			return
		case err != nil:
			web.APIError(w, http.StatusUnauthorized, "unauthenticated", "Sign in again.")
			return
		}
		defer release()
		live := asstapp.StreamLive{Stopping: d.Stopping, Check: recheck, State: func(ctx context.Context) (string, bool, error) {
			return assistant().RunState(ctx, runID)
		}}
		stream := asstapp.StreamSpec{RunID: runID, After: after}
		if err := assistant().ServeStream(ctx, w, stream, live); err != nil && ctx.Err() == nil {
			streamLog.Error("event stream failed", logx.RunID, runID, logx.Err, err)
		}
	}
}

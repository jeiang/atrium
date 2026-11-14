package pages

import (
	"net/http"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/web"
)

func (p *pages) apiEvents(w http.ResponseWriter, r *http.Request) {
	threadID, ok := web.PathID(w, r, "thread_id")
	if !ok {
		return
	}
	runID, ok := web.PathID(w, r, "run_id")
	if !ok {
		return
	}
	p.serveStream(w, r, threadID, runID, func(status int, code, message string, runID int64) {
		if status == http.StatusGone {
			web.APIErrorWith(w, status, code, message, map[string]any{"run_id": runID})
			return
		}
		web.APIError(w, status, code, message)
	})
}

func errorIs(err error, code application.Code) bool {
	return err != nil && application.Classify(err).Code == code
}

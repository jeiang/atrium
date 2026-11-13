package web

import (
	"net/http"
	"strings"

	"github.com/jeiang/atrium/internal/application"
)

func NotModified(w http.ResponseWriter, r *http.Request, revision application.Revision) bool {
	tag := `W/"` + string(revision) + `"`
	h := w.Header()
	h.Set("ETag", tag)
	h.Set("Cache-Control", "private, no-cache")
	h.Add("Vary", "HX-Request")
	for candidate := range strings.SplitSeq(r.Header.Get("If-None-Match"), ",") {
		if candidate = strings.TrimSpace(candidate); candidate == tag || candidate == "*" {
			w.WriteHeader(http.StatusNotModified)
			return true
		}
	}
	return false
}

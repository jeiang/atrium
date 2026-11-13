package api

import (
	"net/http"
	"strings"

	"github.com/jeiang/atrium/internal/application"
)

func notModified(w http.ResponseWriter, r *http.Request, rev application.Revision) bool {
	tag := `W/"` + string(rev) + `"`
	h := w.Header()
	h.Set("ETag", tag)
	h.Set("Cache-Control", "private, no-cache")
	for candidate := range strings.SplitSeq(r.Header.Get("If-None-Match"), ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" || strings.TrimPrefix(candidate, "W/") == strings.TrimPrefix(tag, "W/") {
			w.WriteHeader(http.StatusNotModified)
			return true
		}
	}
	return false
}

package web

import (
	"net/http"
	"strings"

	"github.com/jeiang/atrium/internal/application"
)

func APIRead(w http.ResponseWriter, r *http.Request, revision application.Revision, v any) {
	if revision != "" && conditional(w, r, `W/"`+string(revision)+`"`) {
		return
	}
	JSON(w, http.StatusOK, v)
}

func conditional(w http.ResponseWriter, r *http.Request, tag string) bool {
	h := w.Header()
	h.Set("ETag", tag)
	h.Set("Cache-Control", "private, no-cache")
	for candidate := range strings.SplitSeq(r.Header.Get("If-None-Match"), ",") {
		if candidate = strings.TrimSpace(candidate); candidate == "*" || strings.TrimPrefix(candidate, "W/") == strings.TrimPrefix(tag, "W/") {
			w.WriteHeader(http.StatusNotModified)
			return true
		}
	}
	return false
}

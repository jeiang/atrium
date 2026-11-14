package web

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"strings"

	"github.com/jeiang/atrium/internal/application"
)

func FragmentNotModified(w http.ResponseWriter, r *http.Request, revision application.Revision) bool {
	w.Header().Add("Vary", "HX-Request")
	if !IsPartial(r) {
		return false
	}
	context := fmt.Sprintf("%s\x00%s", revision, CSRFToken(r.Context()))
	return conditional(w, r, fmt.Sprintf(`W/"%x"`, sha256.Sum256([]byte(context))))
}

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

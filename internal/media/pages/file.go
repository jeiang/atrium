package pages

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/jeiang/atrium/internal/media"
	"github.com/jeiang/atrium/internal/web"
)

var fileVideoTypes = map[string]string{
	".mkv": "video/x-matroska",
	".mp4": "video/mp4",
	".m4v": "video/x-m4v",
	".avi": "video/x-msvideo",
	".mov": "video/quicktime",
	".ts":  "video/mp2t",
	".wmv": "video/x-ms-wmv",
}

func fileVideoType(path string) string {
	if kind, ok := fileVideoTypes[strings.ToLower(filepath.Ext(path))]; ok {
		return kind
	}
	return "application/octet-stream"
}

func (s *service) serveFile(w http.ResponseWriter, r *http.Request) {
	rel := web.PathParam(r, "*")
	query := r.URL.Query()
	if !s.links.Verify(rel, web.QueryValue(query, "exp"), web.QueryValue(query, "sig"), s.now()) {
		s.deps.Error(w, r, http.StatusForbidden, "the link is invalid or has expired")
		return
	}
	root, err := s.settings().NeedRoot()
	if err != nil {
		s.deps.Error(w, r, http.StatusNotFound, err.Error())
		return
	}
	full, ok := media.SafeRel(root, rel)
	if !ok {
		s.deps.Error(w, r, http.StatusNotFound, "not found")
		return
	}
	f, err := os.Open(full)
	if err != nil {
		s.deps.Error(w, r, http.StatusNotFound, "not found")
		return
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		s.deps.Error(w, r, http.StatusNotFound, "not found")
		return
	}
	w.Header().Set("Content-Type", fileVideoType(full))
	http.ServeContent(w, r, info.Name(), info.ModTime(), f)
}

package pages

import (
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/jeiang/atrium/internal/media"
	"github.com/jeiang/atrium/internal/web"
)

func fileVideoType(path string, video map[string]bool) string {
	ext := strings.ToLower(filepath.Ext(path))
	if !video[ext] {
		return "application/octet-stream"
	}
	if kind := mime.TypeByExtension(ext); kind != "" {
		return kind
	}
	return "application/octet-stream"
}

func (s *service) serveFile(w http.ResponseWriter, r *http.Request) {
	rel := web.PathParam(r, "*")
	query := r.URL.Query()
	if !s.links.Verify(r.Context(), rel, media.QueryValue(query["exp"]), media.QueryValue(query["sig"]), s.now()) {
		s.deps.Error(w, r, http.StatusForbidden, "the link is invalid or has expired")
		return
	}
	set := s.settings(r.Context())
	root, err := set.NeedRoot()
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
	w.Header().Set("Content-Type", fileVideoType(full, set.VideoExts))
	http.ServeContent(w, r, info.Name(), info.ModTime(), f)
}

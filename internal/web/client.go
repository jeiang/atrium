package web

import (
	"net/http"
	"strings"

	"github.com/jeiang/atrium/internal/urlx"
)

func (s *Server) serveClient(w http.ResponseWriter, r *http.Request) {
	fail := func(status int, detail string) { s.Error(w, r, status, detail) }
	path := r.URL.Path
	if rest, legacy := urlx.StripLegacy(path); legacy {
		if rest == clientWorker {
			s.client.serve(w, r, clientWorker, fail)
			return
		}
		escaped, _ := urlx.StripLegacy(r.URL.EscapedPath())
		localRedirect(w, r, escaped, http.StatusMovedPermanently)
		return
	}
	if target, status, ok := urlx.Moved(r.URL.EscapedPath(), r.URL.RawQuery); ok {
		http.Redirect(w, r, target, status)
		return
	}
	if _, ok := s.client.assets[path]; ok {
		s.client.serve(w, r, path, fail)
		return
	}
	if len(path) > 1 && strings.HasSuffix(path, "/") {
		localRedirect(w, r, strings.TrimRight(r.URL.EscapedPath(), "/"), http.StatusMovedPermanently)
		return
	}
	if path == "/" || !strings.HasPrefix(path, clientAssets) && acceptsHTML(r) {
		s.client.serve(w, r, s.client.shell, fail)
		return
	}
	s.missing(w, r)
}

func localRedirect(w http.ResponseWriter, r *http.Request, escapedPath string, status int) {
	target := "/" + strings.TrimLeft(escapedPath, "/\\")
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	http.Redirect(w, r, target, status)
}

func clientFile(path string) bool {
	_, legacy := urlx.StripLegacy(path)
	return legacy || strings.HasPrefix(path, clientAssets) || path == clientWorker
}

package pages

import (
	"net/http"
	"os"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/jeiang/atrium/internal/media"
	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/web"
)

const (
	artPlaceholderSVG   = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 2 3"><rect width="2" height="3" fill="#80808033"/></svg>`
	artImageCache       = "private, max-age=86400"
	artPlaceholderCache = "private, max-age=600"
	artImageType        = "image/jpeg"
)

func (s *service) registerFiles(r chi.Router) {
	r.Get("/media/art/season/{name}/{number}", s.serveSeasonArt)
	r.Get("/media/art/{kind}/{name}", s.serveArt)
	r.Get("/media/thumb/*", s.serveThumb)
	r.Get(media.FilePrefix+"*", s.serveFile)
}

func writeArtPlaceholder(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Type", "image/svg+xml")
	h.Set("Cache-Control", artPlaceholderCache)
	_, _ = w.Write([]byte(artPlaceholderSVG))
}

func serveArtImage(w http.ResponseWriter, r *http.Request, path string, ok bool) {
	if !ok {
		writeArtPlaceholder(w)
		return
	}
	f, err := os.Open(path)
	if err != nil {
		writeArtPlaceholder(w)
		return
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		writeArtPlaceholder(w)
		return
	}
	h := w.Header()
	h.Set("Content-Type", artImageType)
	h.Set("Cache-Control", artImageCache)
	http.ServeContent(w, r, info.Name(), info.ModTime(), f)
}

func (s *service) serveArt(w http.ResponseWriter, r *http.Request) {
	kind, name := web.PathParam(r, "kind"), web.PathParam(r, "name")
	if (kind != media.KindMovie && kind != media.KindShow) || strings.Contains(name, "/") {
		s.deps.Error(w, r, http.StatusNotFound, "not found")
		return
	}
	path, ok := s.artwork(s.settings(r.Context())).Poster(r.Context(), kind, name, 0)
	serveArtImage(w, r, path, ok)
}

func (s *service) serveSeasonArt(w http.ResponseWriter, r *http.Request) {
	name, raw := web.PathParam(r, "name"), web.PathParam(r, "number")
	number, valid := py.LaxInt64(raw)
	if !valid {
		web.Unprocessable(w, web.BadInt("path", "number", raw))
		return
	}
	if strings.Contains(name, "/") {
		s.deps.Error(w, r, http.StatusNotFound, "not found")
		return
	}
	path, ok := s.artwork(s.settings(r.Context())).Poster(r.Context(), media.KindSeason, name, number)
	serveArtImage(w, r, path, ok)
}

func (s *service) serveThumb(w http.ResponseWriter, r *http.Request) {
	set := s.settings(r.Context())
	root, err := set.NeedRoot()
	if err != nil {
		writeArtPlaceholder(w)
		return
	}
	path, ok := s.artwork(set).Thumbnail(r.Context(), root, web.PathParam(r, "*"))
	serveArtImage(w, r, path, ok)
}

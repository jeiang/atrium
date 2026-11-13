package pages

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/media"
	mediaapp "github.com/jeiang/atrium/internal/media/application"
	"github.com/jeiang/atrium/internal/web"
)

const (
	artPlaceholderSVG   = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 2 3"><rect width="2" height="3" fill="#80808033"/></svg>`
	artImageCache       = "private, max-age=86400"
	artPlaceholderCache = "private, max-age=600"
)

func (s *service) registerFiles(r chi.Router) {
	r.Get("/apps/media/art/season/{name}/{number}", s.serveSeasonArt)
	r.Get("/apps/media/art/{kind}/{name}", s.serveArt)
	r.Get("/apps/media/thumb/*", s.serveThumb)
	r.Get(media.FilePrefix+"*", s.serveFile)
}

func writeArtPlaceholder(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Type", "image/svg+xml")
	h.Set("Cache-Control", artPlaceholderCache)
	_, _ = w.Write([]byte(artPlaceholderSVG))
}

func serveStream(w http.ResponseWriter, r *http.Request, stream *mediaapp.FileStream) {
	defer func() { _ = stream.Close() }()
	w.Header().Set("Content-Type", stream.ContentType)
	http.ServeContent(w, r, stream.Name, stream.ModTime, stream)
}

func (s *service) serveArtOf(w http.ResponseWriter, r *http.Request, in mediaapp.ArtInput) {
	stream, err := s.app.Art(r.Context(), in)
	if err != nil {
		if application.Classify(err).Code != application.NotFound {
			panic(err)
		}
		s.deps.Error(w, r, http.StatusNotFound, "not found")
		return
	}
	if stream == nil {
		writeArtPlaceholder(w)
		return
	}
	w.Header().Set("Cache-Control", artImageCache)
	serveStream(w, r, stream)
}

func (s *service) serveArt(w http.ResponseWriter, r *http.Request) {
	kind := web.PathParam(r, "kind")
	if kind != media.KindMovie && kind != media.KindShow {
		s.deps.Error(w, r, http.StatusNotFound, "not found")
		return
	}
	s.serveArtOf(w, r, mediaapp.ArtInput{Kind: kind, Name: web.PathParam(r, "name")})
}

func (s *service) serveSeasonArt(w http.ResponseWriter, r *http.Request) {
	raw := web.PathParam(r, "number")
	number, valid := laxInt64(raw)
	if !valid {
		web.Unprocessable(w, web.BadInt("path", "number", raw))
		return
	}
	s.serveArtOf(w, r, mediaapp.ArtInput{Kind: media.KindSeason, Name: web.PathParam(r, "name"), Season: number})
}

func (s *service) serveThumb(w http.ResponseWriter, r *http.Request) {
	s.serveArtOf(w, r, mediaapp.ArtInput{Kind: mediaapp.KindThumb, Rel: web.PathParam(r, "*")})
}

func (s *service) serveFile(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	stream, err := s.app.OpenFile(r.Context(), mediaapp.FileInput{Rel: web.PathParam(r, "*"), Exp: media.QueryValue(query["exp"]), Sig: media.QueryValue(query["sig"])})
	if err != nil {
		web.Fail(w, r, err, func(status int) { s.deps.Error(w, r, status, application.Classify(err).Error()) })
		return
	}
	serveStream(w, r, stream)
}

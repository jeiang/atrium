package pages

import (
	"io"
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
	r.Head(media.FilePrefix+"*", s.serveFile)
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
	stream, err := s.app.File(r.Context(), mediaapp.FileInput{
		Rel: web.PathParam(r, "*"), Exp: media.QueryValue(query["exp"]), Sig: media.QueryValue(query["sig"]), Method: r.Method,
		Range: r.Header.Get("Range"), IfRange: r.Header.Get("If-Range"), IfNoneMatch: r.Header.Get("If-None-Match"), IfModifiedSince: r.Header.Get("If-Modified-Since"),
	})
	if err != nil {
		web.Fail(w, r, err, func(status int) { s.deps.Error(w, r, status, application.Classify(err).Error()) })
		return
	}
	defer func() { _ = stream.Body.Close() }()
	for name, values := range stream.Header {
		w.Header()[name] = values
	}
	w.Header().Del("Set-Cookie")
	w.Header().Set("Cache-Control", "private, no-store")
	w.WriteHeader(stream.Status)
	if r.Method != http.MethodHead {
		_, _ = io.CopyBuffer(flushingWriter{w}, stream.Body, make([]byte, 32*1024))
	}
}

type flushingWriter struct{ http.ResponseWriter }

func (w flushingWriter) Write(p []byte) (int, error) {
	n, err := w.ResponseWriter.Write(p)
	if err == nil {
		err = http.NewResponseController(w.ResponseWriter).Flush()
	}
	return n, err
}

package librarymodule

import (
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/jeiang/atrium/internal/application"
	mediaapp "github.com/jeiang/atrium/internal/apps/library/application"
	"github.com/jeiang/atrium/internal/web"
)

const legacyPrefix = "/media"

func fileRoutes(r chi.Router, d *web.Deps, s *mediaapp.Service) {
	serve := func(w http.ResponseWriter, r *http.Request) { serveFile(d, s, w, r) }
	r.Get(mediaapp.FilePrefix+"*", serve)
	r.Head(mediaapp.FilePrefix+"*", serve)
	r.HandleFunc(legacyPrefix, legacyRedirect)
	r.HandleFunc(legacyPrefix+"/*", legacyRedirect)
	r.HandleFunc(legacyPrefix+"/file/*", legacyRedirect)
	d.Public(http.MethodGet, legacyPrefix+"/file/*", legacyRedirect)
	d.Public(http.MethodHead, legacyPrefix+"/file/*", legacyRedirect)
}

func serveFile(d *web.Deps, s *mediaapp.Service, w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	stream, err := s.File(r.Context(), mediaapp.FileInput{
		Rel: web.PathParam(r, "*"), Exp: mediaapp.QueryValue(query["exp"]), Sig: mediaapp.QueryValue(query["sig"]), Method: r.Method,
		Range: r.Header.Get("Range"), IfRange: r.Header.Get("If-Range"), IfNoneMatch: r.Header.Get("If-None-Match"), IfModifiedSince: r.Header.Get("If-Modified-Since"),
	})
	if err != nil {
		web.Fail(w, r, err, func(status int) { d.Error(w, r, status, application.Classify(err).Error()) })
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
		if _, err := io.CopyBuffer(flushingWriter{w}, stream.Body, make([]byte, 32*1024)); err != nil {
			panic(http.ErrAbortHandler)
		}
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

func legacyRedirect(w http.ResponseWriter, r *http.Request) {
	target := "/apps" + r.URL.EscapedPath()
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	status := http.StatusPermanentRedirect
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		status = http.StatusMovedPermanently
	}
	w.Header().Set("Location", target)
	w.WriteHeader(status)
}

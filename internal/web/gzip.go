package web

import (
	"bytes"
	"compress/gzip"
	"net/http"
	"strings"
)

const gzipMinSize = 1024

type gzipMode int

const (
	gzipUndecided gzipMode = iota
	gzipPlain
	gzipOn
)

func Gzip(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, staticPrefix) {
			next.ServeHTTP(w, r)
			return
		}
		g := &gzipWriter{ResponseWriter: w, accept: strings.Contains(r.Header.Get("Accept-Encoding"), "gzip")}
		defer g.finish()
		next.ServeHTTP(g, r)
	})
}

type gzipWriter struct {
	http.ResponseWriter
	accept bool
	mode   gzipMode
	status int
	buf    bytes.Buffer
	gz     *gzip.Writer
}

func (g *gzipWriter) Unwrap() http.ResponseWriter { return g.ResponseWriter }

func (g *gzipWriter) WriteHeader(status int) {
	if g.status == 0 {
		g.status = status
	}
	if g.mode == gzipUndecided && (status < 200 || status == http.StatusNoContent || status == http.StatusNotModified) {
		g.passThrough()
	}
}

func (g *gzipWriter) passThrough() {
	g.mode = gzipPlain
	g.sendHeader()
	_, _ = g.ResponseWriter.Write(g.buf.Bytes())
	g.buf.Reset()
}

func (g *gzipWriter) sendHeader() {
	if g.status == 0 {
		g.status = http.StatusOK
	}
	g.ResponseWriter.WriteHeader(g.status)
}

func (g *gzipWriter) skip() bool {
	h := g.Header()
	if h.Get("Content-Encoding") != "" || h.Get("Content-Range") != "" || h.Get("Accept-Ranges") != "" {
		return true
	}
	kind := h.Get("Content-Type")
	if kind == "" {
		return false
	}
	kind, _, _ = strings.Cut(kind, ";")
	kind = strings.ToLower(strings.TrimSpace(kind))
	switch kind {
	case "text/event-stream":
		return true
	case "application/json", "application/problem+json", "application/javascript", "application/manifest+json", "application/xml", "image/svg+xml":
		return false
	}
	return !strings.HasPrefix(kind, "text/")
}

func (g *gzipWriter) startGzip() {
	if g.skip() {
		g.passThrough()
		return
	}
	h := g.Header()
	h.Add("Vary", "Accept-Encoding")
	if !g.accept {
		g.passThrough()
		return
	}
	g.mode = gzipOn
	h.Del("Content-Length")
	h.Set("Content-Encoding", "gzip")
	g.sendHeader()
	g.gz, _ = gzip.NewWriterLevel(g.ResponseWriter, gzip.BestCompression)
	_, _ = g.gz.Write(g.buf.Bytes())
	g.buf.Reset()
}

func (g *gzipWriter) Write(p []byte) (int, error) {
	switch g.mode {
	case gzipPlain:
		return g.ResponseWriter.Write(p)
	case gzipOn:
		return g.gz.Write(p)
	}
	g.buf.Write(p)
	if g.buf.Len() >= gzipMinSize {
		g.startGzip()
	}
	return len(p), nil
}

func (g *gzipWriter) Flush() {
	if g.mode == gzipUndecided {
		g.startGzip()
	}
	if g.gz != nil {
		_ = g.gz.Flush()
	}
	if f, ok := g.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (g *gzipWriter) finish() {
	switch g.mode {
	case gzipUndecided:
		g.passThrough()
	case gzipOn:
		_ = g.gz.Close()
	}
}

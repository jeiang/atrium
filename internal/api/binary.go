package api

import (
	"io"
	"mime"
	"net/http"
	"strings"
	"time"
)

type Binary struct {
	Name      string            `json:"-"`
	MediaType string            `json:"-"`
	ModTime   time.Time         `json:"-"`
	File      io.ReadSeekCloser `json:"-"`
	Status    int               `json:"-"`
	Header    http.Header       `json:"-"`
	Body      io.ReadCloser     `json:"-"`
}

func (b Binary) close() {
	if b.Body != nil {
		_ = b.Body.Close()
	} else if b.File != nil {
		_ = b.File.Close()
	}
}

func serveBinary(w http.ResponseWriter, r *http.Request, body Binary) {
	defer body.close()
	if body.Body != nil {
		for _, name := range []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges", "ETag", "Last-Modified"} {
			for _, value := range body.Header.Values(name) {
				w.Header().Add(name, value)
			}
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(body.Status)
		if r.Method != http.MethodHead && (body.Status == http.StatusOK || body.Status == http.StatusPartialContent) {
			_, _ = io.Copy(w, body.Body)
		}
		return
	}
	media, _, err := mime.ParseMediaType(body.MediaType)
	if err != nil || media == "" {
		media = "application/octet-stream"
	}
	safe := media == "text/plain" || media == "image/png" || media == "image/jpeg" || media == "image/gif" || media == "image/webp"
	if !safe {
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": body.Name}))
	}
	if strings.HasPrefix(media, "text/") {
		media = mime.FormatMediaType(media, map[string]string{"charset": "utf-8"})
	}
	w.Header().Set("Content-Type", media)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Accept-Ranges", "bytes")
	http.ServeContent(w, r, "", body.ModTime, body.File)
}

package api

import (
	"mime"
	"net/http"
	"os"
	"strings"
	"time"
)

type Binary struct {
	Name      string    `json:"-"`
	MediaType string    `json:"-"`
	ModTime   time.Time `json:"-"`
	File      *os.File  `json:"-"`
}

func serveBinary(w http.ResponseWriter, r *http.Request, body Binary) {
	defer func() { _ = body.File.Close() }()
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

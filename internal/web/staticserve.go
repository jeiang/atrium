package web

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
)

type assetBytes struct {
	content []byte
	etag    string
}

type preparedAsset struct {
	variants    map[string]assetBytes
	contentType string
	immutable   bool
}

type staticHandler struct {
	assets map[string]preparedAsset
}

func newStaticHandler(fsys fs.FS, manifest map[string]string) *staticHandler {
	s := &staticHandler{assets: make(map[string]preparedAsset, len(manifest)+1)}
	load := func(url, name string, immutable bool) {
		asset := preparedAsset{variants: make(map[string]assetBytes, 3), contentType: mime.TypeByExtension(path.Ext(name)), immutable: immutable}
		for _, encoding := range []string{"", "gzip", "br"} {
			suffix := map[string]string{"gzip": ".gz", "br": ".br"}[encoding]
			content, err := fs.ReadFile(fsys, name+suffix)
			if err != nil {
				if encoding != "" && errors.Is(err, fs.ErrNotExist) {
					continue
				}
				panic(err)
			}
			sum := sha256.Sum256(content)
			asset.variants[encoding] = assetBytes{content: content, etag: `"` + hex.EncodeToString(sum[:]) + `"`}
		}
		s.assets[url] = asset
	}
	for _, url := range manifest {
		if !strings.HasPrefix(url, staticPrefix) || !fs.ValidPath(strings.TrimPrefix(url, staticPrefix)) {
			panic(fmt.Errorf("invalid asset path %q", url))
		}
		load(url, strings.TrimPrefix(url, staticPrefix), true)
	}
	load(staticPrefix+"sw.js", "sw.js", false)
	return s
}

func acceptedEncodings(header string) map[string]float64 {
	values := make(map[string]float64)
	for _, part := range strings.Split(header, ",") {
		fields := strings.Split(part, ";")
		name := strings.ToLower(strings.TrimSpace(fields[0]))
		if name == "" {
			continue
		}
		q := 1.0
		for _, field := range fields[1:] {
			key, value, ok := strings.Cut(strings.TrimSpace(field), "=")
			if strings.EqualFold(key, "q") {
				parsed, err := strconv.ParseFloat(value, 64)
				if !ok || err != nil || !(parsed >= 0 && parsed <= 1) {
					q = 0
				} else {
					q = parsed
				}
			}
		}
		values[name] = q
	}
	return values
}

func (s *staticHandler) serve(w http.ResponseWriter, r *http.Request, fail func(status int, detail string)) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		fail(http.StatusMethodNotAllowed, "Method Not Allowed")
		return
	}
	asset, ok := s.assets[r.URL.Path]
	if !ok {
		fail(http.StatusNotFound, "Not Found")
		return
	}
	qualities := acceptedEncodings(r.Header.Get("Accept-Encoding"))
	encoding := ""
	best := 0.0
	for _, candidate := range []string{"br", "gzip", "identity"} {
		key := candidate
		if candidate == "identity" {
			key = ""
		}
		if _, ok := asset.variants[key]; !ok {
			continue
		}
		q, explicit := qualities[candidate]
		if !explicit {
			if candidate == "identity" {
				q = 1
				if qualities["*"] == 0 {
					if _, wildcard := qualities["*"]; wildcard {
						q = 0
					}
				}
			} else {
				q = qualities["*"]
			}
		}
		if q > best {
			encoding, best = key, q
		}
	}
	w.Header().Set("Vary", "Accept-Encoding")
	if best == 0 {
		fail(http.StatusNotAcceptable, "Not Acceptable")
		return
	}
	variant := asset.variants[encoding]
	h := w.Header()
	h.Set("Content-Type", asset.contentType)
	h.Set("ETag", variant.etag)
	h.Set("Cache-Control", "no-cache")
	if asset.immutable {
		h.Set("Cache-Control", "public, max-age=31536000, immutable")
	}
	if r.URL.Path == staticPrefix+"sw.js" {
		h.Set("Service-Worker-Allowed", "/")
	}
	if encoding != "" {
		h.Set("Content-Encoding", encoding)
	}
	for _, tag := range strings.Split(r.Header.Get("If-None-Match"), ",") {
		tag = strings.TrimSpace(tag)
		if tag == "*" || strings.TrimPrefix(tag, "W/") == variant.etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}
	h.Set("Content-Length", strconv.Itoa(len(variant.content)))
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	_, _ = w.Write(variant.content)
}

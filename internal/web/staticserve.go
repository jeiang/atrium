package web

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"net/http"
	"strings"
	"sync"
	"time"
)

type staticHandler struct {
	fsys  fs.FS
	etags sync.Map
}

func newStaticHandler(fsys fs.FS) *staticHandler { return &staticHandler{fsys: fsys} }

func (s *staticHandler) etag(name string, content []byte) string {
	if cached, ok := s.etags.Load(name); ok {
		return cached.(string)
	}
	sum := sha256.Sum256(content)
	tag := `"` + hex.EncodeToString(sum[:16]) + `"`
	s.etags.Store(name, tag)
	return tag
}

func (s *staticHandler) serve(w http.ResponseWriter, r *http.Request, fail func(status int, detail string)) {
	notFound := func() { fail(http.StatusNotFound, "Not Found") }
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		fail(http.StatusMethodNotAllowed, "Method Not Allowed")
		return
	}
	name := strings.TrimPrefix(r.URL.Path, StaticPrefix)
	if !fs.ValidPath(name) || name == "." {
		notFound()
		return
	}
	info, err := fs.Stat(s.fsys, name)
	if err != nil || info.IsDir() {
		notFound()
		return
	}
	content, err := fs.ReadFile(s.fsys, name)
	if err != nil {
		notFound()
		return
	}
	w.Header().Set("ETag", s.etag(name, content))
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(content))
}

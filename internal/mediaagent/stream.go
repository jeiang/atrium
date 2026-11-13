//go:build linux || darwin

package mediaagent

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/mediaagent/protocol"
)

var videoTypes = map[string]string{
	".mkv": "video/x-matroska",
	".mp4": "video/mp4",
	".m4v": "video/x-m4v",
	".avi": "video/x-msvideo",
	".mov": "video/quicktime",
	".ts":  "video/mp2t",
	".wmv": "video/x-ms-wmv",
}

func keptVideo(rel string, size int64) bool {
	return videoTypes[strings.ToLower(path.Ext(rel))] != "" && !protocol.Sample(rel, size)
}

func (a *Agent) serveFile(w http.ResponseWriter, r *http.Request) {
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		problem(w, http.StatusBadRequest, protocol.CodeInvalidRequest, "the query is malformed")
		return
	}
	for k, v := range q {
		if (k != "root" && k != "path") || len(v) != 1 || len(q) != 2 {
			problem(w, http.StatusBadRequest, protocol.CodeInvalidRequest, "the query must have exactly one root and one path")
			return
		}
	}
	root, rel := q.Get("root"), q.Get("path")
	switch root {
	case protocol.RootMovies, protocol.RootTV:
	case protocol.RootDownloads, protocol.RootImport:
		problem(w, http.StatusBadRequest, protocol.CodeUnsafePath, "files are served only from movies and tv")
		return
	default:
		problem(w, http.StatusBadRequest, protocol.CodeInvalidRequest, "the root is unknown")
		return
	}
	a.streamFile(w, r, root, rel)
}

func (a *Agent) streamFile(w http.ResponseWriter, r *http.Request, root, rel string) {
	f, st, err := a.roots.openFile(root, rel)
	switch {
	case err == nil:
	case errors.Is(err, ErrUnsafePath), errors.Is(err, ErrSymlink):
		problem(w, http.StatusBadRequest, protocol.CodeUnsafePath, err.Error())
		return
	case errors.Is(err, ErrNotFound), errors.Is(err, ErrNotRegular):
		problem(w, http.StatusNotFound, protocol.CodeNotFound, "the file does not exist")
		return
	case errors.Is(err, ErrDenied):
		problem(w, http.StatusUnprocessableEntity, protocol.CodePermissions, err.Error())
		return
	default:
		problem(w, http.StatusInternalServerError, protocol.CodeIOError, "the file could not be opened")
		return
	}
	defer func() { _ = f.Close() }()
	id := identity(st)
	kind, ok := videoTypes[strings.ToLower(path.Ext(rel))]
	if !ok {
		kind = "application/octet-stream"
	}
	h := w.Header()
	h.Set("Content-Type", kind)
	sum := sha256.Sum256(fmt.Appendf(nil, "%s:%d:%d:%d:%d", a.state.agentID, id.Device, id.Inode, id.Size, id.MTimeNS))
	h.Set("ETag", fmt.Sprintf(`"%x"`, sum[:16]))
	h.Set("X-Content-Type-Options", "nosniff")
	if strings.Count(r.Header.Get("Range"), ",") >= 16 {
		r.Header.Del("Range")
	}
	http.ServeContent(&contentWriter{ResponseWriter: w, method: r.Method}, r, "", time.Unix(0, id.MTimeNS), f)
}

type contentWriter struct {
	http.ResponseWriter
	method string
	failed bool
}

func (w *contentWriter) WriteHeader(status int) {
	if status >= 400 {
		w.failed = true
		w.Header().Del("Content-Length")
		w.Header().Del("Content-Type")
		problem(w.ResponseWriter, status, protocol.CodeInvalidRequest, "the content request could not be satisfied")
		return
	}
	w.ResponseWriter.WriteHeader(status)
}
func (w *contentWriter) Write(b []byte) (int, error) {
	if w.failed {
		return len(b), nil
	}
	return w.ResponseWriter.Write(b)
}

var _ io.Writer = (*contentWriter)(nil)

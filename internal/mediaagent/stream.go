//go:build linux || darwin

package mediaagent

import (
	"errors"
	"fmt"
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
	h.Set("ETag", fmt.Sprintf(`"%d-%d-%d-%d"`, id.Device, id.Inode, id.Size, id.MTimeNS))
	h.Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, "", time.Unix(0, id.MTimeNS), f)
}

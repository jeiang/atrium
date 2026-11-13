//go:build linux || darwin

package mediaagent

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"

	"github.com/jeiang/atrium/internal/mediaagent/protocol"
)

func thumbnailProblem(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errNoImage), errors.Is(err, ErrNotFound), errors.Is(err, ErrNotRegular):
		problem(w, 404, protocol.CodeNotFound, "the thumbnail does not exist")
	case errors.Is(err, ErrUnsafePath), errors.Is(err, ErrSymlink):
		problem(w, 400, protocol.CodeUnsafePath, "the path is not a library file")
	case errors.Is(err, ErrDenied):
		problem(w, 422, protocol.CodePermissions, "the agent may not read the path")
	case errors.Is(err, context.Canceled):
		problem(w, 503, protocol.CodeDraining, "the agent is draining")
	default:
		problem(w, 503, protocol.CodeUnavailable, "the thumbnail could not be generated")
	}
}

func (a *Agent) serveThumbnail(w http.ResponseWriter, r *http.Request) {
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		problem(w, 400, protocol.CodeInvalidRequest, "the query is malformed")
		return
	}
	for k, v := range q {
		if (k != "root" && k != "path") || len(v) != 1 || len(q) != 2 {
			problem(w, 400, protocol.CodeInvalidRequest, "the query must have exactly one root and one path")
			return
		}
	}
	p := protocol.Path{Root: q.Get("root"), Rel: q.Get("path")}
	if a.roots.byName[p.Root] == nil {
		problem(w, 400, protocol.CodeInvalidRequest, "the root is unknown")
		return
	}
	if !library(p.Root) {
		thumbnailProblem(w, ErrUnsafePath)
		return
	}
	if a.thumbs == nil {
		problem(w, 503, protocol.CodeUnavailable, "thumbnails are not available")
		return
	}
	t, err := a.thumbTarget(p)
	if err != nil {
		thumbnailProblem(w, err)
		return
	}
	defer func() { _ = t.file.Close() }()
	ctx, cancel := a.workContext(r.Context())
	defer cancel()
	if hit, miss := thumbCached(t.final); miss {
		thumbnailProblem(w, errNoImage)
		return
	} else if !hit {
		if err := a.generateThumbnail(ctx, t); err != nil {
			thumbnailProblem(w, err)
			return
		}
	}
	f, err := os.Open(t.final)
	if err != nil {
		thumbnailProblem(w, err)
		return
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		thumbnailProblem(w, err)
		return
	}
	h := w.Header()
	h.Set("Content-Type", protocol.ThumbnailMediaType)
	sum := sha256.Sum256([]byte(a.state.agentID + ":" + info.Name()))
	h.Set("ETag", fmt.Sprintf(`"%x"`, sum[:16]))
	h.Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(&contentWriter{ResponseWriter: w, method: r.Method}, r, "", info.ModTime(), f)
}

func (a *Agent) serveThumbnailPass(w http.ResponseWriter, r *http.Request) {
	var req protocol.ThumbnailPass
	if !decodeRequest(w, r, &req) {
		return
	}
	if len(req.Paths) > protocol.MaxThumbnailPaths {
		problem(w, 413, protocol.CodeRequestTooLarge, "too many thumbnail paths")
		return
	}
	if req.Limit < 0 {
		problem(w, 400, protocol.CodeInvalidRequest, "the limit must not be negative")
		return
	}
	for _, p := range req.Paths {
		if !library(p.Root) || validRel(p.Rel) != nil {
			thumbnailProblem(w, ErrUnsafePath)
			return
		}
	}
	if a.thumbs == nil {
		problem(w, 503, protocol.CodeUnavailable, "thumbnails are not available")
		return
	}
	ctx, cancel := a.workContext(r.Context())
	defer cancel()
	var result protocol.ThumbnailResult
	for _, p := range req.Paths {
		if err := ctx.Err(); err != nil {
			thumbnailProblem(w, err)
			return
		}
		t, err := a.thumbTarget(p)
		if err != nil {
			result.Failed++
			continue
		}
		hit, miss := thumbCached(t.final)
		switch {
		case hit || miss:
		case result.Generated >= req.Limit:
			result.Remaining++
		default:
			err = a.generateThumbnail(ctx, t)
			switch {
			case err == nil:
				result.Generated++
			case errors.Is(err, errNoImage):
				result.Failed++
			default:
				_ = t.file.Close()
				thumbnailProblem(w, err)
				return
			}
		}
		_ = t.file.Close()
	}
	writeJSON(w, 200, result)
}

//go:build linux || darwin

package mediaagent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/mediaagent/protocol"
)

func (a *Agent) libraryRoutes(w http.ResponseWriter, r *http.Request) bool {
	switch r.URL.Path {
	case "/v1/library":
		if allowRead(w, r) {
			a.serveLibrary(w, r)
		}
	case "/v1/thumbnail":
		if allowRead(w, r) {
			a.serveThumbnail(w, r)
		}
	case "/v1/thumbnails/pass":
		if requireMethod(w, r, http.MethodPost) {
			a.serveThumbnailPass(w, r)
		}
	default:
		return false
	}
	return true
}

func scanProblem(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errScanChanged):
		problem(w, 409, protocol.CodeScanChanged, "the library changed during the scan")
	case errors.Is(err, errListLimit):
		problem(w, 413, protocol.CodeRequestTooLarge, "the library exceeds the scan limit")
	case errors.Is(err, context.Canceled):
		problem(w, 503, protocol.CodeDraining, "the agent is draining")
	default:
		problem(w, 500, protocol.CodeIOError, "the library could not be scanned")
	}
}

func (a *Agent) serveLibrary(w http.ResponseWriter, r *http.Request) {
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(q) > 1 || (len(q) == 1 && len(q["cursor"]) != 1) {
		problem(w, 400, protocol.CodeInvalidRequest, "invalid cursor query")
		return
	}
	cursor := q.Get("cursor")
	id, offset := "", 0
	if cursor == "" {
		ctx, cancel := a.workContext(r.Context())
		defer cancel()
		a.library.scanning.Lock()
		scanned := time.Now().UTC()
		nodes, err := a.scanLibrary(ctx)
		a.library.scanning.Unlock()
		if err != nil {
			scanProblem(w, err)
			return
		}
		var b [16]byte
		_, _ = rand.Read(b[:])
		id = hex.EncodeToString(b[:])
		a.library.Lock()
		a.expireLibrary()
		if len(a.library.snapshots) >= 2 {
			oldest := ""
			for k, s := range a.library.snapshots {
				if oldest == "" || s.scanned.Before(a.library.snapshots[oldest].scanned) {
					oldest = k
				}
			}
			delete(a.library.snapshots, oldest)
		}
		a.library.snapshots[id] = librarySnapshot{nodes: nodes, scanned: scanned}
		a.library.Unlock()
	} else {
		var tail string
		var ok bool
		id, tail, ok = strings.Cut(cursor, ":")
		n, err := strconv.Atoi(tail)
		if !ok || err != nil || n < 0 {
			problem(w, 410, protocol.CodeSnapshotExpired, "the library snapshot has expired")
			return
		}
		offset = n
	}
	a.library.Lock()
	a.expireLibrary()
	s, ok := a.library.snapshots[id]
	a.library.Unlock()
	if !ok || offset > len(s.nodes) {
		problem(w, 410, protocol.CodeSnapshotExpired, "the library snapshot has expired")
		return
	}
	end := min(offset+protocol.LibraryPageSize, len(s.nodes))
	next := ""
	if end < len(s.nodes) {
		next = fmt.Sprintf("%s:%d", id, end)
	}
	writeJSON(w, 200, protocol.LibraryPage{SnapshotID: id, ScannedAt: s.scanned, RootRevision: a.roots.revision, Nodes: s.nodes[offset:end], Next: next})
}

func (a *Agent) expireLibrary() {
	if a.library.snapshots == nil {
		a.library.snapshots = map[string]librarySnapshot{}
	}
	for id, s := range a.library.snapshots {
		if time.Since(s.scanned) > 5*time.Minute {
			delete(a.library.snapshots, id)
		}
	}
}

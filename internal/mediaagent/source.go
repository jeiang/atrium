//go:build linux || darwin

package mediaagent

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jeiang/atrium/internal/mediaagent/protocol"
	"golang.org/x/sys/unix"
)

type importSnapshot struct {
	entries []protocol.Path
	created time.Time
}
type importSnapshots struct {
	sync.Mutex
	snapshots map[string]importSnapshot
}

func decodeRequest(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := protocol.Decode(r.Body, protocol.MaxBody, v); err != nil {
		status := http.StatusBadRequest
		code := protocol.CodeInvalidRequest
		if errors.Is(err, protocol.ErrTooLarge) {
			status = http.StatusRequestEntityTooLarge
			code = protocol.CodeRequestTooLarge
		}
		problem(w, status, code, "the request body is invalid")
		return false
	}
	return true
}
func fileProblem(w http.ResponseWriter, err error) {
	code := codeOf(err)
	status := http.StatusInternalServerError
	switch code {
	case protocol.CodeUnsafePath:
		status = 400
	case protocol.CodeNotFound:
		status = 404
	case protocol.CodeDestinationExists, protocol.CodeIdentityChanged:
		status = 409
	case protocol.CodeCrossDevice, protocol.CodePermissions, protocol.CodeUnsupportedFile:
		status = 422
	case protocol.CodeRequestTooLarge:
		status = 413
	}
	problem(w, status, code, "the filesystem request was refused")
}

var errListLimit = errors.New("the listing exceeds the limit")

func (a *Agent) children(p protocol.Path, limit int) ([]protocol.Entry, error) {
	f, err := a.roots.directory(p)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	entries, err := f.ReadDir(limit + 1)
	if err != nil {
		return nil, err
	}
	if len(entries) > limit {
		return nil, errListLimit
	}
	out := make([]protocol.Entry, 0, len(entries))
	for _, e := range entries {
		var st unix.Stat_t
		if validRel(e.Name()) != nil {
			return nil, ErrUnsafePath
		}
		if err := unix.Fstatat(int(f.Fd()), e.Name(), &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return nil, walkError(err)
		}
		switch st.Mode & unix.S_IFMT {
		case unix.S_IFDIR, unix.S_IFREG:
		default:
			return nil, ErrNotRegular
		}
		out = append(out, protocol.Entry{Name: e.Name(), Dir: st.Mode&unix.S_IFMT == unix.S_IFDIR, Size: st.Size, Identity: identity(st)})
	}
	slices.SortFunc(out, func(a, b protocol.Entry) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}
func (a *Agent) collect(p protocol.Path) ([]protocol.File, []protocol.File, error) {
	var files, dirs []protocol.File
	var walk func(protocol.Path, int) error
	walk = func(p protocol.Path, depth int) error {
		if depth > 64 {
			return ErrUnsafePath
		}
		st, err := a.roots.stat(p)
		if err != nil {
			return err
		}

		switch st.Mode & unix.S_IFMT {
		case unix.S_IFREG:
			if len(files) >= 1000 {
				return errListLimit
			}
			files = append(files, protocol.File{Path: p, Identity: identity(st)})
		case unix.S_IFDIR:
			if len(files)+len(dirs) >= 5000 {
				return errListLimit
			}
			dirs = append(dirs, protocol.File{Path: p, Identity: identity(st)})
			entries, err := a.children(p, 1000)
			if err != nil {
				return err
			}
			for _, e := range entries {
				if err := walk(protocol.Path{Root: p.Root, Rel: path.Join(p.Rel, e.Name)}, depth+1); err != nil {
					return err
				}
			}
		default:
			return ErrNotRegular
		}
		return nil
	}
	err := walk(p, 0)
	return files, dirs, err
}
func listProblem(w http.ResponseWriter, err error) {
	if errors.Is(err, errListLimit) {
		problem(w, 413, protocol.CodeRequestTooLarge, "the listing exceeds the limit")
		return
	}
	fileProblem(w, err)
}
func (a *Agent) serveSource(w http.ResponseWriter, r *http.Request) {
	var req protocol.SourceRequest
	if !decodeRequest(w, r, &req) {
		return
	}
	if req.Source.Root != protocol.RootDownloads && req.Source.Root != protocol.RootImport {
		fileProblem(w, ErrUnsafePath)
		return
	}
	files, _, err := a.collect(req.Source)
	if err != nil {
		listProblem(w, err)
		return
	}
	writeJSON(w, 200, protocol.Source{Files: files})
}
func (a *Agent) serveEntries(w http.ResponseWriter, r *http.Request) {
	var req protocol.EntriesRequest
	if !decodeRequest(w, r, &req) {
		return
	}
	if !library(req.Path.Root) {
		fileProblem(w, ErrUnsafePath)
		return
	}
	entries, err := a.children(req.Path, 5000)
	if err != nil {
		listProblem(w, err)
		return
	}
	writeJSON(w, 200, protocol.Entries{Entries: entries})
}
func (a *Agent) serveImports(w http.ResponseWriter, r *http.Request) {
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		problem(w, 400, protocol.CodeInvalidRequest, "the query is malformed")
		return
	}
	if len(q) > 1 || (len(q) == 1 && len(q["cursor"]) != 1) {
		problem(w, 400, protocol.CodeInvalidRequest, "invalid cursor query")
		return
	}
	a.imports.Lock()
	defer a.imports.Unlock()
	if a.imports.snapshots == nil {
		a.imports.snapshots = map[string]importSnapshot{}
	}
	for id, s := range a.imports.snapshots {
		if time.Since(s.created) > 5*time.Minute {
			delete(a.imports.snapshots, id)
		}
	}
	cursor := q.Get("cursor")
	id := ""
	offset := 0
	if cursor == "" {
		entries, err := a.children(protocol.Path{Root: protocol.RootImport}, 5000)
		if err != nil {
			listProblem(w, err)
			return
		}
		paths := make([]protocol.Path, 0, len(entries))
		for _, e := range entries {
			paths = append(paths, protocol.Path{Root: protocol.RootImport, Rel: e.Name})
		}
		if len(a.imports.snapshots) >= 2 {
			var oldest string
			for k, s := range a.imports.snapshots {
				if oldest == "" || s.created.Before(a.imports.snapshots[oldest].created) {
					oldest = k
				}
			}
			delete(a.imports.snapshots, oldest)
		}
		var b [16]byte
		_, _ = rand.Read(b[:])
		id = hex.EncodeToString(b[:])
		a.imports.snapshots[id] = importSnapshot{entries: paths, created: time.Now()}
	} else {
		var tail string
		var ok bool
		id, tail, ok = strings.Cut(cursor, ":")
		n, err := strconv.Atoi(tail)
		if !ok || err != nil || n < 0 {
			problem(w, 410, protocol.CodeSnapshotExpired, "the import snapshot has expired")
			return
		}
		offset = n
	}
	s, ok := a.imports.snapshots[id]
	if !ok || offset > len(s.entries) {
		problem(w, 410, protocol.CodeSnapshotExpired, "the import snapshot has expired")
		return
	}
	end := min(offset+500, len(s.entries))
	next := ""
	if end < len(s.entries) {
		next = fmt.Sprintf("%s:%d", id, end)
	}
	writeJSON(w, 200, protocol.ImportPage{Entries: s.entries[offset:end], Next: next})
}

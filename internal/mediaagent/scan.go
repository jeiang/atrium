//go:build linux || darwin

package mediaagent

import (
	"context"
	"errors"
	"os"
	"path"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jeiang/atrium/internal/mediaagent/protocol"
	"golang.org/x/sys/unix"
)

const maxLibraryNodes = 200000

var (
	errScanChanged = errors.New("the library changed during the scan")
	errRootFailed  = errors.New("a library root is unavailable")
)

type librarySnapshot struct {
	nodes   []protocol.Node
	scanned time.Time
}
type librarySnapshots struct {
	sync.Mutex
	scanning  sync.Mutex
	snapshots map[string]librarySnapshot
}

func (a *Agent) workContext(parent context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	stop := context.AfterFunc(a.work, cancel)
	return ctx, func() { stop(); cancel() }
}

func (a *Agent) scanLibrary(ctx context.Context) ([]protocol.Node, error) {
	if !a.roots.healthy() {
		return nil, errRootFailed
	}
	nodes := []protocol.Node{}
	var walk func(root string, dir *os.File, rel string, depth int) error
	walk = func(root string, dir *os.File, rel string, depth int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		entries, err := dir.ReadDir(-1)
		if err != nil {
			return err
		}
		slices.SortFunc(entries, func(a, b os.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
		for _, e := range entries {
			name := e.Name()
			if validRel(name) != nil {
				continue
			}
			var st unix.Stat_t
			if err := unix.Fstatat(int(dir.Fd()), name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
				if errors.Is(err, unix.ENOENT) {
					return errScanChanged
				}
				return err
			}
			child := path.Join(rel, name)
			kind := st.Mode & unix.S_IFMT
			if kind != unix.S_IFREG && kind != unix.S_IFDIR {
				continue
			}
			if len(nodes) >= maxLibraryNodes {
				return errListLimit
			}
			nodes = append(nodes, protocol.Node{Root: root, Rel: child, Dir: kind == unix.S_IFDIR, Size: st.Size, Identity: identity(st)})
			if kind == unix.S_IFREG {
				continue
			}
			if depth >= 64 {
				return errListLimit
			}
			fd, err := openat(int(dir.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC)
			if errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR) {
				return errScanChanged
			}
			if err != nil {
				return err
			}
			sub := os.NewFile(uintptr(fd), child)
			var now unix.Stat_t
			if err := unix.Fstat(fd, &now); err != nil || !sameObject(identity(now), identity(st)) {
				_ = sub.Close()
				if err != nil {
					return err
				}
				return errScanChanged
			}
			err = walk(root, sub, child, depth+1)
			_ = sub.Close()
			if err != nil {
				return err
			}
		}
		return nil
	}
	for _, name := range []string{protocol.RootMovies, protocol.RootTV} {
		fd, err := openat(a.roots.byName[name].fd, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC)
		if err != nil {
			return nil, errRootFailed
		}
		dir := os.NewFile(uintptr(fd), name)
		err = walk(name, dir, "", 1)
		_ = dir.Close()
		if err != nil {
			return nil, err
		}
	}
	if !a.roots.healthy() {
		return nil, errRootFailed
	}
	return nodes, nil
}

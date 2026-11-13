//go:build linux || darwin

package mediaagent

import (
	"cmp"
	"context"
	"errors"
	"io"
	"os"
	"path"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jeiang/atrium/internal/mediaagent/protocol"
	"golang.org/x/sys/unix"
)

const (
	maxLibraryNodes = 200000
	scanBatch       = 1024
)

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
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			names, err := dir.Readdirnames(scanBatch)
			if errors.Is(err, io.EOF) {
				return nil
			}
			if err != nil {
				if depth > 1 && denied(err) {
					return nil
				}
				return err
			}
			for _, name := range names {
				if err := a.scanEntry(root, dir, rel, name, depth, &nodes, walk); err != nil {
					return err
				}
			}
		}
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
	slices.SortFunc(nodes, func(x, y protocol.Node) int {
		return cmp.Or(strings.Compare(x.Root, y.Root), strings.Compare(x.Rel, y.Rel))
	})
	return nodes, nil
}

func denied(err error) bool {
	return errors.Is(err, unix.EACCES) || errors.Is(err, unix.EPERM)
}

func (a *Agent) scanEntry(root string, dir *os.File, rel, name string, depth int, nodes *[]protocol.Node, walk func(string, *os.File, string, int) error) error {
	if validRel(name) != nil {
		return nil
	}
	var st unix.Stat_t
	if err := unix.Fstatat(int(dir.Fd()), name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		switch {
		case errors.Is(err, unix.ENOENT):
			return errScanChanged
		case denied(err):
			return nil
		}
		return err
	}
	child := path.Join(rel, name)
	kind := st.Mode & unix.S_IFMT
	if kind != unix.S_IFREG && kind != unix.S_IFDIR {
		return nil
	}
	if len(*nodes) >= maxLibraryNodes {
		return errListLimit
	}
	node := protocol.Node{Root: root, Rel: child, Dir: kind == unix.S_IFDIR, Size: st.Size, Identity: identity(st)}
	if kind == unix.S_IFREG {
		*nodes = append(*nodes, node)
		return nil
	}
	if depth >= 64 {
		return errListLimit
	}
	fd, err := openat(int(dir.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC)
	switch {
	case errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR):
		return errScanChanged
	case err != nil && denied(err):
		return nil
	case err != nil:
		return err
	}
	sub := os.NewFile(uintptr(fd), child)
	defer func() { _ = sub.Close() }()
	var now unix.Stat_t
	if err := unix.Fstat(fd, &now); err != nil {
		return err
	}
	if !sameObject(identity(now), identity(st)) {
		return errScanChanged
	}
	*nodes = append(*nodes, node)
	return walk(root, sub, child, depth+1)
}

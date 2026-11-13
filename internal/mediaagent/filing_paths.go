//go:build linux || darwin

package mediaagent

import (
	"errors"
	"os"
	"strings"

	"github.com/jeiang/atrium/internal/mediaagent/protocol"
	"golang.org/x/sys/unix"
)

func (rs *roots) parent(p protocol.Path) (int, string, error) {
	if err := validRel(p.Rel); err != nil {
		return -1, "", err
	}
	r := rs.byName[p.Root]
	if r == nil {
		return -1, "", ErrUnsafePath
	}
	fd, err := unix.Dup(r.fd)
	if err != nil {
		return -1, "", err
	}
	parts := strings.Split(p.Rel, "/")
	for _, part := range parts[:len(parts)-1] {
		next, err := openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC)
		if errors.Is(err, unix.ENOTDIR) && isSymlink(fd, part) {
			err = unix.ELOOP
		}
		_ = unix.Close(fd)
		if err != nil {
			return -1, "", walkError(err)
		}
		fd = next
	}
	return fd, parts[len(parts)-1], nil
}
func (rs *roots) stat(p protocol.Path) (unix.Stat_t, error) {
	var st unix.Stat_t
	fd, leaf, err := rs.parent(p)
	if err != nil {
		return st, err
	}
	defer func() { _ = unix.Close(fd) }()
	err = unix.Fstatat(fd, leaf, &st, unix.AT_SYMLINK_NOFOLLOW)
	if err != nil {
		return st, walkError(err)
	}
	if st.Mode&unix.S_IFMT == unix.S_IFLNK {
		return st, ErrSymlink
	}
	return st, nil
}
func (rs *roots) directory(p protocol.Path) (*os.File, error) {
	r := rs.byName[p.Root]
	if r == nil {
		return nil, ErrUnsafePath
	}
	var fd int
	var err error
	if p.Rel == "" {
		fd, err = openat(r.fd, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC)
	} else {
		var parent int
		var leaf string
		parent, leaf, err = rs.parent(p)
		if err != nil {
			return nil, err
		}
		fd, err = openat(parent, leaf, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC)
		if errors.Is(err, unix.ENOTDIR) && isSymlink(parent, leaf) {
			err = unix.ELOOP
		}
		_ = unix.Close(parent)
	}
	if err != nil {
		return nil, walkError(err)
	}
	return os.NewFile(uintptr(fd), p.Root), nil
}
func sameObject(a, b protocol.Identity) bool { return a.Device == b.Device && a.Inode == b.Inode }
func library(root string) bool               { return root == protocol.RootMovies || root == protocol.RootTV }
func sourceRoot(kind string) string {
	if kind == protocol.SourceTorrent {
		return protocol.RootDownloads
	}
	return protocol.RootImport
}
func codeOf(err error) string {
	switch {
	case errors.Is(err, ErrUnsafePath), errors.Is(err, ErrSymlink):
		return protocol.CodeUnsafePath
	case errors.Is(err, ErrNotFound), errors.Is(err, unix.ENOENT):
		return protocol.CodeNotFound
	case errors.Is(err, unix.EEXIST), errors.Is(err, unix.ENOTEMPTY):
		return protocol.CodeDestinationExists
	case errors.Is(err, unix.EXDEV):
		return protocol.CodeCrossDevice
	case errors.Is(err, ErrDenied), errors.Is(err, unix.EACCES), errors.Is(err, unix.EPERM):
		return protocol.CodePermissions
	case errors.Is(err, ErrNotRegular):
		return protocol.CodeUnsupportedFile
	}
	return protocol.CodeIOError
}

//go:build linux || darwin

package mediaagent

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/jeiang/atrium/internal/mediaagent/protocol"
	"golang.org/x/sys/unix"
)

var (
	ErrUnsafePath = errors.New("the path is not a safe relative path")
	ErrNotFound   = errors.New("the file does not exist")
	ErrNotRegular = errors.New("the path is not a regular file")
	ErrSymlink    = errors.New("the path crosses a symbolic link")
	ErrDenied     = errors.New("the agent may not read the path")
)

type root struct {
	name string
	path string
	fd   int
	dev  uint64
	ino  uint64
}

type roots struct {
	byName   map[string]*root
	revision string
}

func openRoots(c Config) (*roots, error) {
	rs := &roots{byName: map[string]*root{}}
	sum := sha256.New()
	for _, n := range c.Roots.named() {
		r, err := openRoot(n[0], n[1])
		if err != nil {
			rs.close()
			return nil, err
		}
		rs.byName[r.name] = r
		_, _ = fmt.Fprintf(sum, "%s\x00%d\x00%d\n", r.name, r.dev, r.ino)
	}
	rs.revision = hex.EncodeToString(sum.Sum(nil))
	return rs, nil
}

func openRoot(name, path string) (*root, error) {
	var st unix.Stat_t
	if err := unix.Lstat(path, &st); err != nil {
		return nil, fmt.Errorf("roots.%s: %w", name, err)
	}
	if st.Mode&unix.S_IFMT == unix.S_IFLNK {
		return nil, fmt.Errorf("roots.%s must not be a symbolic link", name)
	}
	if st.Mode&unix.S_IFMT != unix.S_IFDIR {
		return nil, fmt.Errorf("roots.%s must be a directory", name)
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("roots.%s: %w", name, err)
	}
	if err := unix.Fstat(fd, &st); err != nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("roots.%s: %w", name, err)
	}
	return &root{name: name, path: path, fd: fd, dev: uint64(st.Dev), ino: st.Ino}, nil
}

func (rs *roots) close() {
	for _, r := range rs.byName {
		_ = unix.Close(r.fd)
	}
	rs.byName = nil
}

func (rs *roots) healthy() bool {
	for _, r := range rs.byName {
		var held, now unix.Stat_t
		if unix.Fstat(r.fd, &held) != nil || unix.Lstat(r.path, &now) != nil {
			return false
		}
		if now.Mode&unix.S_IFMT != unix.S_IFDIR || uint64(now.Dev) != r.dev || now.Ino != r.ino || held.Nlink == 0 {
			return false
		}
	}
	return true
}

func validRel(rel string) error {
	if rel == "" || !utf8.ValidString(rel) || strings.ContainsAny(rel, "\x00\\") || strings.HasPrefix(rel, "/") {
		return ErrUnsafePath
	}
	for c := range strings.SplitSeq(rel, "/") {
		if c == "" || c == "." || c == ".." {
			return ErrUnsafePath
		}
	}
	return nil
}

func (rs *roots) openFile(name, rel string) (*os.File, unix.Stat_t, error) {
	var st unix.Stat_t
	r, ok := rs.byName[name]
	if !ok {
		return nil, st, ErrUnsafePath
	}
	if err := validRel(rel); err != nil {
		return nil, st, err
	}
	parts := strings.Split(rel, "/")
	dir := r.fd
	closeDir := func() {
		if dir != r.fd {
			_ = unix.Close(dir)
		}
	}
	for _, c := range parts[:len(parts)-1] {
		fd, err := openat(dir, c, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC)
		if errors.Is(err, unix.ENOTDIR) && isSymlink(dir, c) {
			err = unix.ELOOP
		}
		closeDir()
		if err != nil {
			return nil, st, walkError(err)
		}
		dir = fd
	}
	fd, err := openat(dir, parts[len(parts)-1], unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_NOCTTY|unix.O_CLOEXEC)
	closeDir()
	if err != nil {
		return nil, st, walkError(err)
	}
	if err := unix.Fstat(fd, &st); err != nil {
		_ = unix.Close(fd)
		return nil, st, err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG {
		_ = unix.Close(fd)
		return nil, st, ErrNotRegular
	}
	if err := unix.SetNonblock(fd, false); err != nil {
		_ = unix.Close(fd)
		return nil, st, err
	}
	return os.NewFile(uintptr(fd), name+":"+strconv.FormatUint(st.Ino, 10)), st, nil
}

func openat(dir int, name string, flags int) (int, error) {
	for {
		fd, err := unix.Openat(dir, name, flags, 0)
		if !errors.Is(err, unix.EINTR) {
			return fd, err
		}
	}
}

func isSymlink(dir int, name string) bool {
	var st unix.Stat_t
	return unix.Fstatat(dir, name, &st, unix.AT_SYMLINK_NOFOLLOW) == nil && st.Mode&unix.S_IFMT == unix.S_IFLNK
}

func walkError(err error) error {
	switch {
	case errors.Is(err, unix.ELOOP), errors.Is(err, unix.EMLINK):
		return ErrSymlink
	case errors.Is(err, unix.ENOENT), errors.Is(err, unix.ENOTDIR):
		return ErrNotFound
	case errors.Is(err, unix.EACCES), errors.Is(err, unix.EPERM):
		return ErrDenied
	}
	return err
}

func identity(st unix.Stat_t) protocol.Identity {
	return protocol.Identity{Device: uint64(st.Dev), Inode: st.Ino, Size: st.Size, MTimeNS: st.Mtim.Nano()}
}

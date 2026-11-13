package db

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

type AlreadyRunning struct{ Path string }

func (e *AlreadyRunning) Error() string {
	return "db: " + e.Path + " is in use by a running atrium serve or destructive database command; stop it first"
}

type ServeLock struct {
	f     *os.File
	given string
	id    identity
}

type identity struct {
	path     string
	exists   bool
	dev, ino uint64
}

func identify(path string) (identity, error) {
	canonical, err := CanonicalPath(path)
	if err != nil {
		return identity{}, err
	}
	info, err := os.Stat(canonical)
	if errors.Is(err, fs.ErrNotExist) {
		return identity{path: canonical}, nil
	}
	if err != nil {
		return identity{}, err
	}
	if !info.Mode().IsRegular() {
		return identity{}, fmt.Errorf("db: %s is not a regular file", canonical)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return identity{}, fmt.Errorf("db: cannot read the file identity of %s", canonical)
	}
	if st.Nlink > 1 {
		return identity{}, fmt.Errorf("db: %s has %d hard links; a database must have exactly one name", canonical, st.Nlink)
	}
	return identity{path: canonical, exists: true, dev: uint64(st.Dev), ino: st.Ino}, nil
}

func TryServeLock(ctx context.Context, path string) (*ServeLock, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o777); err != nil {
		return nil, err
	}
	id, err := identify(path)
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(id.path+".serve.lock", os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	ok, err := tryFlock(f)
	if err == nil && !ok {
		err = &AlreadyRunning{Path: id.path}
	}
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	l := &ServeLock{f: f, given: path, id: id}
	if err := l.Check(); err != nil {
		_ = f.Close()
		return nil, err
	}
	return l, nil
}

func (l *ServeLock) Check() error {
	if l == nil {
		return nil
	}
	now, err := identify(l.given)
	if err != nil {
		return err
	}
	if now.path != l.id.path || (l.id.exists && (!now.exists || now.dev != l.id.dev || now.ino != l.id.ino)) {
		return fmt.Errorf("db: %s changed identity while it was being opened", l.id.path)
	}
	l.id = now
	return nil
}

func (l *ServeLock) Close() error {
	if l == nil || l.f == nil {
		return nil
	}
	err := l.f.Close()
	l.f = nil
	return err
}

package db

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

type Staged struct {
	path string
	dest string
}

func Stage(dest string, perm fs.FileMode) (*Staged, error) {
	dir := filepath.Dir(dest)
	if err := os.MkdirAll(dir, 0o777); err != nil {
		return nil, err
	}
	f, err := os.CreateTemp(dir, "."+filepath.Base(dest)+".*")
	if err != nil {
		return nil, err
	}
	path := f.Name()
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return nil, err
	}
	if err := os.Chmod(path, perm); err != nil {
		_ = os.Remove(path)
		return nil, err
	}
	return &Staged{path: path, dest: dest}, nil
}

func (s *Staged) Path() string { return s.path }

func (s *Staged) Discard() { removeSidecars(s.path) }

func (s *Staged) PublishLink() error {
	if _, err := os.Lstat(s.dest); err == nil {
		return fmt.Errorf("db: destination %s already exists", s.dest)
	}
	if err := os.Link(s.path, s.dest); err != nil {
		return err
	}
	if err := syncDir(filepath.Dir(s.dest)); err != nil {
		return err
	}
	s.Discard()
	return nil
}

func (s *Staged) PublishRename() error {
	if err := os.Rename(s.path, s.dest); err != nil {
		return err
	}
	return syncDir(filepath.Dir(s.dest))
}

func removeSidecars(path string) {
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		_ = os.Remove(path + suffix)
	}
}

func syncDir(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	return errors.Join(f.Sync(), f.Close())
}

func Verify(ctx context.Context, path string, key Key, want map[string]int64) error {
	c, err := OpenFile(path, key)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	if err := IntegrityCheck(ctx, c); err != nil {
		return err
	}
	if want == nil {
		return nil
	}
	got, err := TableCounts(ctx, c)
	if err != nil {
		return err
	}
	for name, n := range want {
		if got[name] != n {
			return fmt.Errorf("table %s has %d rows in the copy, %d in the source", name, got[name], n)
		}
	}
	for name := range got {
		if _, ok := want[name]; !ok {
			return fmt.Errorf("table %s exists only in the copy", name)
		}
	}
	return nil
}

package db

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
)

type LockBusy struct{ Resource string }

func (e *LockBusy) Error() string {
	return "db: " + e.Resource + " is held by another invocation"
}

var resourceKey = regexp.MustCompile(`^[a-z][a-z0-9-]*(/[A-Za-z0-9._@:+-]+)*$`)

func ValidResource(key string) bool { return resourceKey.MatchString(key) }

func CanonicalPath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err == nil {
		return real, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	dir, err := filepath.EvalSymlinks(filepath.Dir(abs))
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, filepath.Base(abs)), nil
}

type LockSet struct{ files []*os.File }

func TryResourceLocks(path string, resources []string) (*LockSet, error) {
	canonical, err := CanonicalPath(path)
	if err != nil {
		return nil, err
	}
	dir := canonical + ".locks"
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, err
	}
	keys := slices.Clone(resources)
	slices.Sort(keys)
	keys = slices.Compact(keys)
	set := &LockSet{}
	for _, key := range keys {
		if !ValidResource(key) {
			_ = set.Close()
			return nil, fmt.Errorf("db: invalid resource key %q", key)
		}
		sum := sha256.Sum256([]byte(key))
		f, err := os.OpenFile(filepath.Join(dir, hex.EncodeToString(sum[:])+".lock"), os.O_RDWR|os.O_CREATE, 0o600)
		if err != nil {
			_ = set.Close()
			return nil, err
		}
		ok, err := tryFlock(f)
		if err != nil || !ok {
			_ = f.Close()
			_ = set.Close()
			if err != nil {
				return nil, err
			}
			return nil, &LockBusy{Resource: key}
		}
		set.files = append(set.files, f)
	}
	return set, nil
}

func (l *LockSet) Close() error {
	if l == nil {
		return nil
	}
	var errs []error
	for i := len(l.files) - 1; i >= 0; i-- {
		errs = append(errs, l.files[i].Close())
	}
	l.files = nil
	return errors.Join(errs...)
}

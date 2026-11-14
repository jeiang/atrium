package db

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"time"
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

type LockSet struct {
	files []*os.File
	names []string
}

type heldKey struct{}

func WithHeld(ctx context.Context, set *LockSet) context.Context {
	if set == nil || len(set.names) == 0 {
		return ctx
	}
	held := map[string]bool{}
	if outer, ok := ctx.Value(heldKey{}).(map[string]bool); ok {
		maps.Copy(held, outer)
	}
	for _, name := range set.names {
		held[name] = true
	}
	return context.WithValue(ctx, heldKey{}, held)
}

func Held(ctx context.Context, resource string) bool {
	held, _ := ctx.Value(heldKey{}).(map[string]bool)
	return held[resource]
}

func ResourceLocks(ctx context.Context, path string, resources []string, wait time.Duration) (*LockSet, error) {
	wanted := slices.DeleteFunc(slices.Clone(resources), func(r string) bool { return Held(ctx, r) })
	ticker := time.NewTicker(lockPollInterval)
	defer ticker.Stop()
	var timeout <-chan time.Time
	if wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		timeout = timer.C
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		set, err := TryResourceLocks(path, wanted)
		var busy *LockBusy
		if !errors.As(err, &busy) {
			return set, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timeout:
			return nil, err
		case <-ticker.C:
		}
	}
}

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
		set.files, set.names = append(set.files, f), append(set.names, key)
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

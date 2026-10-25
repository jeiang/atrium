package davmirror

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
)

type Object struct {
	Name string
	Body []byte
}

type Collection struct {
	Name    string
	Objects []Object
}

type Stats struct {
	Written int `json:"written"`
	Removed int `json:"removed"`
	Kept    int `json:"kept"`
}

func safeName(name, ext string) string {
	base := filepath.Base(strings.TrimRight(name, "/"))
	if base == "" || base == "." || base == ".." || base == string(filepath.Separator) || strings.HasPrefix(base, ".") {
		sum := sha256.Sum256([]byte(name))
		base = hex.EncodeToString(sum[:8])
	}
	if ext != "" && !strings.HasSuffix(base, ext) {
		base += ext
	}
	return base
}

func Apply(dir, ext string, collections []Collection) (Stats, error) {
	var st Stats
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return st, err
	}
	keep := map[string]bool{}
	for _, c := range collections {
		sub := safeName(c.Name, "")
		keep[sub] = true
		path := filepath.Join(dir, sub)
		if err := os.MkdirAll(path, 0o750); err != nil {
			return st, err
		}
		wanted := map[string]bool{}
		for _, o := range c.Objects {
			name := safeName(o.Name, ext)
			wanted[name] = true
			target := filepath.Join(path, name)
			if old, err := os.ReadFile(target); err == nil && bytes.Equal(old, o.Body) {
				st.Kept++
				continue
			}
			if err := writeAtomic(target, o.Body); err != nil {
				return st, err
			}
			st.Written++
		}
		n, err := prune(path, ext, wanted)
		st.Removed += n
		if err != nil {
			return st, err
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return st, err
	}
	for _, e := range entries {
		if !e.IsDir() || keep[e.Name()] {
			continue
		}
		path := filepath.Join(dir, e.Name())
		n, err := prune(path, ext, nil)
		st.Removed += n
		if err != nil {
			return st, err
		}
		_ = os.Remove(path)
	}
	return st, nil
}

func prune(path, ext string, wanted map[string]bool) (int, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ext) || wanted[e.Name()] {
			continue
		}
		if err := os.Remove(filepath.Join(path, e.Name())); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

func writeAtomic(target string, body []byte) error {
	f, err := os.CreateTemp(filepath.Dir(target), ".partial-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.Write(body); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Chmod(tmp, 0o640); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, target)
}

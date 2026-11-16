package backup

import (
	"archive/tar"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

type Listed struct {
	Name     string
	Size     int64
	Modified time.Time
	Header   Header
	Problem  error
}

func List(dir string, limit int) ([]Listed, int, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = root.Close() }()
	d, err := root.Open(".")
	if err != nil {
		return nil, 0, err
	}
	entries, err := d.ReadDir(-1)
	_ = d.Close()
	if err != nil {
		return nil, 0, err
	}
	var found []Listed
	for _, e := range entries {
		if matched, _ := filepath.Match(archivePattern, e.Name()); !matched || !e.Type().IsRegular() {
			continue
		}
		info, err := e.Info()
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, 0, err
		}
		found = append(found, Listed{Name: e.Name(), Size: info.Size(), Modified: info.ModTime()})
	}
	slices.SortFunc(found, func(a, b Listed) int {
		if c := b.Modified.Compare(a.Modified); c != 0 {
			return c
		}
		return strings.Compare(b.Name, a.Name)
	})
	page := found[:min(max(limit, 0), len(found))]
	for i := range page {
		page[i].Header, page[i].Problem = peek(root, page[i].Name)
	}
	return page, len(found), nil
}

func peek(root *os.Root, name string) (Header, error) {
	f, err := root.Open(name)
	if err != nil {
		return Header{}, ErrNotArchive
	}
	defer func() { _ = f.Close() }()
	if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() {
		return Header{}, ErrNotArchive
	}
	h, _, err := readHeader(tar.NewReader(f))
	if err != nil && !errors.Is(err, ErrNewerFormat) && !errors.Is(err, ErrOldFormat) {
		return Header{}, ErrNotArchive
	}
	return h, err
}

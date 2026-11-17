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

const ExamineMax = 1000

type Listed struct {
	Name     string
	Size     int64
	Modified time.Time
	Prunable bool
	Header   Header
	Problem  error
}

type Listing struct {
	Archives   []Listed
	Total      int
	Unexamined int
}

func List(dir string, limit int) (Listing, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return Listing{}, err
	}
	defer func() { _ = root.Close() }()
	d, err := root.Open(".")
	if err != nil {
		return Listing{}, err
	}
	entries, err := d.ReadDir(-1)
	_ = d.Close()
	if err != nil {
		return Listing{}, err
	}
	var candidates []Listed
	for _, e := range entries {
		name := e.Name()
		if !e.Type().IsRegular() || strings.HasPrefix(name, ".") || strings.Contains(name, `\`) {
			continue
		}
		info, err := e.Info()
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return Listing{}, err
		}
		prunable, _ := filepath.Match(archivePattern, name)
		candidates = append(candidates, Listed{Name: name, Size: info.Size(), Modified: info.ModTime(), Prunable: prunable})
	}
	slices.SortFunc(candidates, func(a, b Listed) int {
		if c := b.Modified.Compare(a.Modified); c != 0 {
			return c
		}
		return strings.Compare(b.Name, a.Name)
	})
	out := Listing{Unexamined: max(len(candidates)-ExamineMax, 0)}
	for _, c := range candidates[:min(len(candidates), ExamineMax)] {
		h, err := peek(root, c.Name)
		if errors.Is(err, ErrNotArchive) {
			continue
		}
		out.Total++
		if len(out.Archives) < limit {
			c.Header, c.Problem = h, err
			out.Archives = append(out.Archives, c)
		}
	}
	return out, nil
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

package calendar

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/jeiang/atrium/internal/py"
)

func pathParts(p string) []string {
	var parts []string
	for _, part := range strings.Split(p, "/") {
		if part != "" && part != "." {
			parts = append(parts, part)
		}
	}
	return parts
}

func pathText(p string) string {
	parts := pathParts(p)
	text := strings.Join(parts, "/")
	switch {
	case strings.HasPrefix(p, "/"):
		return "/" + text
	case text == "":
		return "."
	}
	return text
}

type osFailure struct {
	class string
	text  string
}

func (e *osFailure) Error() string { return e.text }

func (e *osFailure) PyClass() string { return e.class }

func osClass(err error) string {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "FileNotFoundError"
	case errors.Is(err, fs.ErrPermission):
		return "PermissionError"
	case errors.Is(err, syscall.EISDIR):
		return "IsADirectoryError"
	case errors.Is(err, syscall.ENOTDIR):
		return "NotADirectoryError"
	}
	return "OSError"
}

func collectionName(root, file string) (string, error) {
	label := filepath.Join(filepath.Dir(file), "displayname")
	if info, err := os.Stat(label); err == nil && info.Mode().IsRegular() {
		data, err := os.ReadFile(label)
		if err == nil {
			if !utf8.Valid(data) {
				return "", &osFailure{"UnicodeDecodeError", "'utf-8' codec can't decode bytes"}
			}
			if text := py.Strip(string(data)); text != "" {
				return text, nil
			}
		}
	}
	rel, _ := filepath.Rel(root, file)
	parts := pathParts(filepath.ToSlash(rel))
	if len(parts) > 1 {
		return parts[0], nil
	}
	rootParts := pathParts(filepath.ToSlash(root))
	if len(rootParts) == 0 {
		return "", nil
	}
	return rootParts[len(rootParts)-1], nil
}

// MirrorEvents is mirror_events: the events of every .ics file under directory that overlap the window, with the
// notes of what could not be read or expanded. A non-directory is NotConfigured.
func MirrorEvents(directory string, ws, we time.Time, tz *time.Location, target string) ([]Row, []string, error) {
	root := pathText(directory)
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		return nil, nil, NotConfigured(fmt.Sprintf("calendar mirror %s is not a directory", root))
	}
	var files []string
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err == nil && path != root && strings.HasSuffix(d.Name(), ".ics") {
			files = append(files, path)
		}
		return nil
	})
	slices.SortFunc(files, func(a, b string) int {
		return slices.Compare(pathParts(filepath.ToSlash(a)), pathParts(filepath.ToSlash(b)))
	})
	wanted := py.Lower(target)
	out, notes := []Row{}, []string{}
	for _, file := range files {
		name, err := collectionName(root, file)
		if err != nil {
			return nil, nil, err
		}
		rel, _ := filepath.Rel(root, file)
		if wanted != "" && wanted != py.Lower(name) && wanted != py.Lower(pathParts(filepath.ToSlash(rel))[0]) {
			continue
		}
		data, err := os.ReadFile(file)
		if err != nil {
			notes = append(notes, fmt.Sprintf("%s: unreadable (%s)", filepath.Base(file), osClass(err)))
			continue
		}
		rows, err := EventsInICS(data, name, ws, we, tz, &notes)
		var bad valueError
		if errors.As(err, &bad) {
			notes = append(notes, fmt.Sprintf("%s: unreadable (ValueError)", filepath.Base(file)))
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		out = append(out, rows...)
	}
	return out, notes, nil
}

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
)

func osClass(err error) string {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "file not found"
	case errors.Is(err, fs.ErrPermission):
		return "permission denied"
	case errors.Is(err, syscall.EISDIR):
		return "is a directory"
	case errors.Is(err, syscall.ENOTDIR):
		return "not a directory"
	}
	return "I/O error"
}

func collectionName(root, file string) string {
	rel, _ := filepath.Rel(root, file)
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) > 1 {
		return parts[0]
	}
	base := filepath.Base(filepath.Clean(root))
	if base == "." || base == string(filepath.Separator) {
		return ""
	}
	return base
}

func MirrorEvents(directory string, ws, we time.Time, tz *time.Location, target string, maxOccurrences int) ([]Row, []string, error) {
	root := filepath.Clean(directory)
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		return nil, nil, NotConfigured(fmt.Sprintf("calendar mirror %s is not a directory", root))
	}
	var files []string
	if err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err == nil && path != root && strings.HasSuffix(d.Name(), ".ics") {
			files = append(files, path)
		}
		return nil
	}); err != nil {
		return nil, nil, err
	}
	slices.SortFunc(files, func(a, b string) int {
		return strings.Compare(filepath.ToSlash(a), filepath.ToSlash(b))
	})
	wanted := strings.ToLower(target)
	out, notes := []Row{}, []string{}
	for _, file := range files {
		name := collectionName(root, file)
		rel, _ := filepath.Rel(root, file)
		parts := strings.Split(filepath.ToSlash(rel), "/")
		if wanted != "" && wanted != strings.ToLower(name) && (len(parts) == 0 || wanted != strings.ToLower(parts[0])) {
			continue
		}
		data, err := os.ReadFile(file)
		if err != nil {
			notes = append(notes, fmt.Sprintf("%s: unreadable: %s", filepath.Base(file), osClass(err)))
			continue
		}
		rows, err := EventsInICS(data, name, ws, we, tz, &notes, maxOccurrences)
		var bad valueError
		if errors.As(err, &bad) {
			notes = append(notes, fmt.Sprintf("%s: unreadable: invalid calendar data", filepath.Base(file)))
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		out = append(out, rows...)
	}
	return out, notes, nil
}

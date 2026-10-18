package py

import (
	"os"
	"strings"
)

// Resolve is pathlib.Path(p).resolve(), CPython 3.14's posixpath.realpath(p, strict=False): a relative p is
// taken against the working directory, symlinks are followed, a ".." removes the last resolved component, a
// component that cannot be inspected is kept as it is, and a symlink loop leaves the link in place. The result
// is absolute and normalized.
func Resolve(p string) string {
	path := "/"
	if !strings.HasPrefix(p, "/") {
		wd, err := os.Getwd()
		if err != nil {
			wd = "/"
		}
		path = wd
	}
	type marker struct{ resolved bool }
	rest := []any{}
	push := func(s string) {
		parts := strings.Split(s, "/")
		for i := len(parts) - 1; i >= 0; i-- {
			rest = append(rest, parts[i])
		}
	}
	push(p)
	partCount := len(rest)
	seen := map[string]*string{}
	for partCount > 0 {
		top := rest[len(rest)-1]
		rest = rest[:len(rest)-1]
		if m, ok := top.(marker); ok && m.resolved {
			link := rest[len(rest)-1].(string)
			rest = rest[:len(rest)-1]
			resolved := path
			seen[link] = &resolved
			continue
		}
		name := top.(string)
		partCount--
		switch name {
		case "", ".":
			continue
		case "..":
			if i := strings.LastIndex(path, "/"); i > 0 {
				path = path[:i]
			} else {
				path = "/"
			}
			continue
		}
		newpath := path + "/" + name
		if path == "/" {
			newpath = "/" + name
		}
		if info, err := os.Lstat(newpath); err != nil || info.Mode()&os.ModeSymlink == 0 {
			path = newpath
			continue
		}
		if cached, ok := seen[newpath]; ok {
			path = newpath
			if cached != nil {
				path = *cached
			}
			continue
		}
		target, err := os.Readlink(newpath)
		if err != nil {
			path = newpath
			continue
		}
		if strings.HasPrefix(target, "/") {
			path = "/"
		}
		seen[newpath] = nil
		rest = append(rest, newpath, marker{resolved: true})
		before := len(rest)
		push(target)
		partCount += len(rest) - before
	}
	return path
}

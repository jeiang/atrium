package py

import (
	"os"
	"path/filepath"
	"strings"
)

const maxSymlinks = 40

// Resolve is pathlib.Path(p).resolve() (os.path.realpath, non-strict): a relative p is taken against the
// working directory, every symlink is followed before a following "..", and components that do not exist
// are kept as they are.
func Resolve(p string) string {
	if !filepath.IsAbs(p) {
		wd, err := os.Getwd()
		if err != nil {
			return filepath.Clean(p)
		}
		p = wd + "/" + p
	}
	pending := strings.Split(p, "/")
	resolved := ""
	links := 0
	for len(pending) > 0 {
		name := pending[0]
		pending = pending[1:]
		switch name {
		case "", ".":
			continue
		case "..":
			resolved = filepath.Dir(resolved)
			if resolved == "." || resolved == "/" {
				resolved = ""
			}
			continue
		}
		next := resolved + "/" + name
		target, err := os.Readlink(next)
		if err != nil {
			resolved = next
			continue
		}
		links++
		if links > maxSymlinks {
			return next + "/" + strings.Join(pending, "/")
		}
		if strings.HasPrefix(target, "/") {
			resolved = ""
		}
		pending = append(strings.Split(target, "/"), pending...)
	}
	if resolved == "" {
		return "/"
	}
	return resolved
}

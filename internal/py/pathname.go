package py

import "strings"

// PathName is PurePosixPath(p).name: the last component, ignoring empty and "." components.
func PathName(p string) string {
	name := ""
	for part := range strings.SplitSeq(p, "/") {
		if part != "" && part != "." {
			name = part
		}
	}
	return name
}

// PathSuffix is the .suffix of a path component (CPython 3.14): from the last dot of the name once leading dots
// are skipped, empty without one.
func PathSuffix(name string) string {
	rest := strings.TrimLeft(name, ".")
	if i := strings.LastIndex(rest, "."); i >= 0 {
		return rest[i:]
	}
	return ""
}

// SplitExt is posixpath.splitext(p): the extension is everything from the last dot of the final component,
// ignoring leading dots.
func SplitExt(p string) (root, ext string) {
	sep := strings.LastIndex(p, "/")
	dot := strings.LastIndex(p, ".")
	if dot > sep {
		for i := sep + 1; i < dot; i++ {
			if p[i] != '.' {
				return p[:dot], p[dot:]
			}
		}
	}
	return p, ""
}

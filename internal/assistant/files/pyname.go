package files

import (
	"strings"

	"github.com/jeiang/atrium/internal/py"
)

func urlScheme(url string) (scheme, rest string) {
	i := strings.IndexByte(url, ':')
	if i <= 0 || !isASCIILetter(url[0]) {
		return "", url
	}
	for j := range i {
		if c := url[j]; !isASCIILetter(c) && (c < '0' || c > '9') && c != '+' && c != '-' && c != '.' {
			return "", url
		}
	}
	return strings.ToLower(url[:i]), url[i+1:]
}

func isASCIILetter(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }

func urlPath(rest string) string {
	if strings.HasPrefix(rest, "//") {
		rest = rest[2:]
		if i := strings.IndexAny(rest, "/?#"); i >= 0 {
			rest = rest[i:]
		} else {
			rest = ""
		}
	}
	if i := strings.IndexByte(rest, '#'); i >= 0 {
		rest = rest[:i]
	}
	if i := strings.IndexByte(rest, '?'); i >= 0 {
		rest = rest[:i]
	}
	return rest
}

// guessType is mimetypes.guess_type(name) (strict) over CPython's built-in table only.
func guessType(name string) (string, bool) {
	scheme, rest := urlScheme(name)
	path := name
	if len(scheme) > 1 {
		path = urlPath(rest)
		if scheme == "data" {
			return guessData(path)
		}
	}
	base, ext := py.SplitExt(path)
	for {
		replaced, ok := mimeSuffixes[py.Lower(ext)]
		if !ok {
			break
		}
		base, ext = py.SplitExt(base + replaced)
	}
	if _, ok := mimeEncodings[ext]; ok {
		_, ext = py.SplitExt(base)
	}
	media, ok := mimeTypes[py.Lower(ext)]
	return media, ok
}

func guessData(url string) (string, bool) {
	comma := strings.IndexByte(url, ',')
	if comma < 0 {
		return "", false
	}
	kind := url[:comma]
	if semi := strings.IndexByte(url[:comma], ';'); semi >= 0 {
		kind = url[:semi]
	}
	if strings.Contains(kind, "=") || !strings.Contains(kind, "/") {
		kind = "text/plain"
	}
	return kind, true
}

func pairs(list [][2]string) map[string]string {
	m := make(map[string]string, len(list))
	for _, p := range list {
		m[p[0]] = p[1]
	}
	return m
}

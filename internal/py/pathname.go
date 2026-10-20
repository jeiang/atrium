package py

import "strings"

func PathName(p string) string {
	name := ""
	for part := range strings.SplitSeq(p, "/") {
		if part != "" && part != "." {
			name = part
		}
	}
	return name
}

func PathSuffix(name string) string {
	rest := strings.TrimLeft(name, ".")
	if i := strings.LastIndex(rest, "."); i >= 0 {
		return rest[i:]
	}
	return ""
}

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

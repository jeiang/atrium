package serverops

import (
	"slices"
	"strings"
	"unicode"
)

func SplitHosts(value string) []string {
	var out []string
	for _, host := range strings.FieldsFunc(value, func(r rune) bool { return r == ',' || unicode.IsSpace(r) }) {
		if host != "" && !slices.Contains(out, host) {
			out = append(out, host)
		}
	}
	return out
}

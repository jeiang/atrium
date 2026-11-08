package textx

import "unicode/utf8"

// Clip returns the first n runes of s.
func Clip(s string, n int) string {
	for i := range s {
		if n == 0 {
			return s[:i]
		}
		n--
	}
	return s
}

// SplitLines splits s on Python-style line boundaries.
func SplitLines(s string) []string {
	var out []string
	start := 0
	for i, r := range s {
		if i < start || !isLineBreak(r) {
			continue
		}
		out = append(out, s[start:i])
		start = i + utf8.RuneLen(r)
		if r == '\r' && start < len(s) && s[start] == '\n' {
			start++
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

func isLineBreak(r rune) bool {
	switch r {
	case '\n', '\r', '\v', '\f', 0x1C, 0x1D, 0x1E, 0x85, 0x2028, 0x2029:
		return true
	}
	return false
}

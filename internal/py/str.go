package py

import (
	"strings"
	"unicode/utf8"
)

// IsSpace is str.isspace for one code point.
func IsSpace(r rune) bool {
	if r < utf8.RuneSelf {
		return r >= '\t' && r <= '\r' || r >= 0x1C && r <= ' '
	}
	return spaceTable[r]
}

// Strip is str.strip().
func Strip(s string) string { return strings.TrimFunc(s, IsSpace) }

// Split is str.split() with no separator.
func Split(s string) []string { return strings.FieldsFunc(s, IsSpace) }

func isLineBreak(r rune) bool {
	switch r {
	case '\n', '\r', '\v', '\f', 0x1C, 0x1D, 0x1E, 0x85, 0x2028, 0x2029:
		return true
	}
	return false
}

// SplitLines is str.splitlines().
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

// Clip is s[:n] counted in code points.
func Clip(s string, n int) string {
	for i := range s {
		if n == 0 {
			return s[:i]
		}
		n--
	}
	return s
}

// Lower is str.lower() up to final-sigma context: U+0130 expands to "i" plus a combining dot as in CPython.
func Lower(s string) string { return strings.ToLower(strings.ReplaceAll(s, "\u0130", "i\u0307")) }

// Casefold is str.casefold.
func Casefold(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r + 'a' - 'A')
		case r < utf8.RuneSelf:
			b.WriteRune(r)
		default:
			if f, ok := pyFold[r]; ok {
				b.WriteString(f)
			} else {
				b.WriteRune(r)
			}
		}
	}
	return b.String()
}

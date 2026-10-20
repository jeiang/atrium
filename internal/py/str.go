package py

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

func IsSpace(r rune) bool {
	if r < utf8.RuneSelf {
		return r >= '\t' && r <= '\r' || r >= 0x1C && r <= ' '
	}
	return spaceTable[r]
}

func Strip(s string) string { return strings.TrimFunc(s, IsSpace) }

func Split(s string) []string { return strings.FieldsFunc(s, IsSpace) }

func isLineBreak(r rune) bool {
	switch r {
	case '\n', '\r', '\v', '\f', 0x1C, 0x1D, 0x1E, 0x85, 0x2028, 0x2029:
		return true
	}
	return false
}

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

func Clip(s string, n int) string {
	for i := range s {
		if n == 0 {
			return s[:i]
		}
		n--
	}
	return s
}

func Lower(s string) string { return strings.ToLower(strings.ReplaceAll(s, "\u0130", "i\u0307")) }

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

func Upper(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r < utf8.RuneSelf:
			if r >= 'a' && r <= 'z' {
				r -= 'a' - 'A'
			}
			b.WriteRune(r)
		default:
			if u, ok := pyUpper[r]; ok {
				b.WriteString(u)
			} else {
				b.WriteRune(r)
			}
		}
	}
	return b.String()
}

func IsDigit(r rune) bool {
	if r < utf8.RuneSelf {
		return r >= '0' && r <= '9'
	}
	return unicode.Is(DigitTable, r)
}

func IsDigits(s string) bool {
	return s != "" && strings.IndexFunc(s, func(r rune) bool { return !IsDigit(r) }) < 0
}

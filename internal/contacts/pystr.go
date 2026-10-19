package contacts

import (
	"bytes"
	"strings"
	"unicode/utf8"
)

// Casefold is Python's str.casefold.
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

// ReadText is Path.read_text(encoding="utf-8-sig", errors="replace"): UTF-8 without a leading BOM, with one
// U+FFFD per maximal invalid subpart, and universal newlines.
func ReadText(data []byte) string {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	var b strings.Builder
	b.Grow(len(data))
	for i := 0; i < len(data); {
		c := data[i]
		if c < utf8.RuneSelf {
			b.WriteByte(c)
			i++
			continue
		}
		n, lo, hi := 0, byte(0x80), byte(0xBF)
		switch {
		case c >= 0xC2 && c <= 0xDF:
			n = 1
		case c == 0xE0:
			n, lo = 2, 0xA0
		case c == 0xED:
			n, hi = 2, 0x9F
		case c >= 0xE1 && c <= 0xEF:
			n = 2
		case c == 0xF0:
			n, lo = 3, 0x90
		case c == 0xF4:
			n, hi = 3, 0x8F
		case c >= 0xF1 && c <= 0xF3:
			n = 3
		}
		j := i + 1
		for k := 0; k < n && j < len(data); k++ {
			if k == 0 && (data[j] < lo || data[j] > hi) || k > 0 && (data[j] < 0x80 || data[j] > 0xBF) {
				break
			}
			j++
		}
		if n == 0 || j-i <= n {
			b.WriteRune(utf8.RuneError)
		} else {
			b.Write(data[i:j])
		}
		i = j
	}
	s := strings.ReplaceAll(b.String(), "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}

package py

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

func Repr(s string) string {
	quote := byte('\'')
	if strings.ContainsRune(s, '\'') && !strings.ContainsRune(s, '"') {
		quote = '"'
	}
	var b strings.Builder
	b.WriteByte(quote)
	for _, r := range s {
		switch {
		case r == '\\' || r == rune(quote):
			b.WriteByte('\\')
			b.WriteRune(r)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r == utf8.RuneError || strconv.IsPrint(r):
			b.WriteRune(r)
		case r < 0x100:
			fmt.Fprintf(&b, `\x%02x`, r)
		case r < 0x10000:
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			fmt.Fprintf(&b, `\U%08x`, r)
		}
	}
	b.WriteByte(quote)
	return b.String()
}

func ReprBytes(b []byte) string {
	quote := byte('\'')
	if strings.IndexByte(string(b), '\'') >= 0 && strings.IndexByte(string(b), '"') < 0 {
		quote = '"'
	}
	var s strings.Builder
	s.WriteByte('b')
	s.WriteByte(quote)
	for _, c := range b {
		switch {
		case c == '\\' || c == quote:
			s.WriteByte('\\')
			s.WriteByte(c)
		case c == '\t':
			s.WriteString(`\t`)
		case c == '\n':
			s.WriteString(`\n`)
		case c == '\r':
			s.WriteString(`\r`)
		case c < 0x20 || c >= 0x7f:
			fmt.Fprintf(&s, `\x%02x`, c)
		default:
			s.WriteByte(c)
		}
	}
	s.WriteByte(quote)
	return s.String()
}

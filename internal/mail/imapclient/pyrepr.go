package imapclient

import (
	"fmt"
	"strings"
	"unicode"
)

func bytesRepr(b []byte) string {
	quote := byte('\'')
	if strings.IndexByte(string(b), '\'') >= 0 && strings.IndexByte(string(b), '"') < 0 {
		quote = '"'
	}
	var s strings.Builder
	s.WriteString("b")
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

func (d datum) repr() string {
	switch {
	case d.none:
		return "None"
	case d.tuple:
		return "(" + bytesRepr(d.text) + ", " + bytesRepr(d.lit) + ")"
	}
	return bytesRepr(d.text)
}

func listRepr(data []datum) string {
	parts := make([]string, len(data))
	for i, d := range data {
		parts[i] = d.repr()
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// latin1 is bytes.decode("latin1").
func latin1(b []byte) string {
	r := make([]rune, len(b))
	for i, c := range b {
		r[i] = rune(c)
	}
	return string(r)
}

// str is str(d) after d.decode("latin1") for bytes, as imap.py joins response data.
func (d datum) str() string {
	if d.none || d.tuple {
		return d.repr()
	}
	return latin1(d.text)
}

func pyIsSpace(r rune) bool { return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f) }

func pySplit(s string) []string {
	out := strings.FieldsFunc(s, pyIsSpace)
	if out == nil {
		out = []string{}
	}
	return out
}

func pyStrip(s string) string { return strings.TrimFunc(s, pyIsSpace) }

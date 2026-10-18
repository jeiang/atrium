package mail

import (
	"encoding/base64"
	"slices"
	"strings"
	"unicode/utf16"

	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/rx"
)

var utf7ShiftRE = rx.MustCompile(`&([^-]*)-`, 0)

// UTF7Decode is imap.utf7_decode: modified UTF-7 (RFC 3501 5.1.3) to text.
func UTF7Decode(value string) (string, error) {
	var failed error
	out, err := utf7ShiftRE.SubFunc(value, func(m *rx.Match) string {
		chunk := m.Group(1)
		if chunk == "" {
			return "&"
		}
		chunk = strings.ReplaceAll(chunk, ",", "/")
		chunk += strings.Repeat("=", (4-len([]rune(chunk))%4)%4)
		raw, err := py.B64Decode(chunk)
		if err == nil {
			var text string
			if text, err = py.DecodeUTF16BE(raw); err == nil {
				return text
			}
		}
		if failed == nil {
			failed = err
		}
		return ""
	}, 0)
	if err != nil {
		return "", err
	}
	return out, failed
}

// UTF7Encode is imap.utf7_encode: text to modified UTF-7.
func UTF7Encode(value string) string {
	var out strings.Builder
	var pending []rune
	flush := func() {
		if len(pending) == 0 {
			return
		}
		units := utf16.Encode(pending)
		raw := make([]byte, 2*len(units))
		for i, u := range units {
			raw[2*i], raw[2*i+1] = byte(u>>8), byte(u)
		}
		chunk := strings.TrimRight(base64.StdEncoding.EncodeToString(raw), "=")
		out.WriteString("&" + strings.ReplaceAll(chunk, "/", ",") + "-")
		pending = pending[:0]
	}
	for _, ch := range value {
		switch {
		case ch == '&':
			flush()
			out.WriteString("&-")
		case ch >= 0x20 && ch <= 0x7e:
			flush()
			out.WriteRune(ch)
		default:
			pending = append(pending, ch)
		}
	}
	flush()
	return out.String()
}

// CollapseLabels is imap.collapse_labels: the user labels (no leading backslash) that have no sub-label in the
// set, sorted.
func CollapseLabels(labels []string) []string {
	user := map[string]bool{}
	for _, l := range labels {
		if !strings.HasPrefix(l, `\`) {
			user[l] = true
		}
	}
	out := []string{}
	for l := range user {
		leaf := true
		for other := range user {
			if other != l && strings.HasPrefix(other, l+"/") {
				leaf = false
				break
			}
		}
		if leaf {
			out = append(out, l)
		}
	}
	slices.Sort(out)
	return out
}

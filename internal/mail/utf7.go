package mail

import (
	"encoding/base64"
	"slices"
	"strings"
	"unicode/utf16"

	"regexp"

	"github.com/jeiang/atrium/internal/py"
)

var utf7ShiftRE = regexp.MustCompile(`&([^-]*)-`)

func UTF7Decode(value string) (string, error) {
	var failed error
	var b strings.Builder
	last := 0
	for _, loc := range utf7ShiftRE.FindAllStringSubmatchIndex(value, -1) {
		chunk := value[loc[2]:loc[3]]
		b.WriteString(value[last:loc[0]])
		last = loc[1]
		if chunk == "" {
			b.WriteString("&")
			continue
		}
		chunk = strings.ReplaceAll(chunk, ",", "/")
		chunk += strings.Repeat("=", (4-len([]rune(chunk))%4)%4)
		raw, err := py.B64Decode(chunk)
		if err == nil {
			var text string
			if text, err = py.DecodeUTF16BE(raw); err == nil {
				b.WriteString(text)
				continue
			}
		}
		if failed == nil {
			failed = err
		}
	}
	b.WriteString(value[last:])
	return b.String(), failed
}

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

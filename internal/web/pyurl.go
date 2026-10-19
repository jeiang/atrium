package web

import (
	"net/http"
	"strings"

	"github.com/jeiang/atrium/internal/py"
)

// sameOrigin is the Python same_origin: no Origin header passes; a "null"
// origin or one whose authority differs from the Host header fails.
func sameOrigin(r *http.Request) bool {
	values := r.Header["Origin"]
	if len(values) == 0 {
		return true
	}
	origin := values[0]
	if origin == "null" {
		return false
	}
	parts, err := py.URLSplit(origin)
	return err == nil && strings.EqualFold(parts.Netloc, r.Host)
}

func hexVal(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	}
	return -1
}

// unquotePlus is urllib.parse.unquote_plus on bytes: an invalid escape stays literal.
func unquotePlus(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '+':
			b.WriteByte(' ')
		case c == '%' && i+2 < len(s) && hexVal(s[i+1]) >= 0 && hexVal(s[i+2]) >= 0:
			b.WriteByte(byte(hexVal(s[i+1])<<4 | hexVal(s[i+2])))
			i += 2
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// formField is the first non-blank value of name in an urlencoded body, as
// parse_qs(body).get(name).
func formField(body, name string) (string, bool) {
	for pair := range strings.SplitSeq(body, "&") {
		key, value, found := strings.Cut(pair, "=")
		if !found || value == "" {
			continue
		}
		if unquotePlus(key) == name {
			return unquotePlus(value), true
		}
	}
	return "", false
}

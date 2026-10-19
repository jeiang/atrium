package web

import (
	"net/http"
	"strings"
)

func isSchemeChar(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '+' || c == '-' || c == '.'
}

// SplitURL is urlsplit(url) without scheme and fragment: the netloc, path and query. ok is false where Python
// raises ValueError (unbalanced IPv6 brackets).
func SplitURL(url string) (netloc, path, query string, ok bool) {
	url = strings.TrimLeftFunc(url, func(r rune) bool { return r <= ' ' })
	url = strings.NewReplacer("\t", "", "\r", "", "\n", "").Replace(url)
	if i := strings.IndexByte(url, ':'); i > 0 && (url[0] >= 'a' && url[0] <= 'z' || url[0] >= 'A' && url[0] <= 'Z') {
		valid := true
		for j := 1; j < i; j++ {
			valid = valid && isSchemeChar(url[j])
		}
		if valid {
			url = url[i+1:]
		}
	}
	if strings.HasPrefix(url, "//") {
		rest := url[2:]
		end := strings.IndexAny(rest, "/?#")
		if end < 0 {
			end = len(rest)
		}
		netloc, url = rest[:end], rest[end:]
		if strings.Contains(netloc, "[") != strings.Contains(netloc, "]") {
			return "", "", "", false
		}
	}
	url, _, _ = strings.Cut(url, "#")
	path, query, _ = strings.Cut(url, "?")
	return netloc, path, query, true
}

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
	netloc, _, _, ok := SplitURL(origin)
	return ok && strings.EqualFold(netloc, r.Host)
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

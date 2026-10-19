package webfetch

import (
	"slices"
	"strconv"
	"strings"

	"github.com/jeiang/atrium/internal/mail/mime"
	"github.com/jeiang/atrium/internal/py"
)

// parseParams is email._parseparam: the ";"-separated parts of a header value, a ";" inside quotes kept, a
// name lower-cased and the whitespace around "=" removed.
func parseParams(value string) []string {
	s := ";" + value
	var out []string
	for strings.HasPrefix(s, ";") {
		s = s[1:]
		end := strings.IndexByte(s, ';')
		for end > 0 && (strings.Count(s[:end], `"`)-strings.Count(s[:end], `\"`))%2 == 1 {
			next := strings.IndexByte(s[end+1:], ';')
			if next < 0 {
				end = -1
			} else {
				end += 1 + next
			}
		}
		if end < 0 {
			end = len(s)
		}
		f := s[:end]
		if name, val, ok := strings.Cut(f, "="); ok {
			f = py.Lower(py.Strip(name)) + "=" + py.Strip(val)
		}
		out = append(out, py.Strip(f))
		s = s[end:]
	}
	return out
}

// unquoteParam is email.utils.unquote.
func unquoteParam(s string) string {
	if len([]rune(s)) > 1 {
		if strings.HasPrefix(s, `"`) && strings.HasSuffix(s, `"`) {
			return strings.ReplaceAll(strings.ReplaceAll(s[1:len(s)-1], `\\`, `\`), `\"`, `"`)
		}
		if strings.HasPrefix(s, "<") && strings.HasSuffix(s, ">") {
			return s[1 : len(s)-1]
		}
	}
	return s
}

// continuation is one piece of an RFC 2231 parameter: its number (-1 when it has none), its text and whether
// the text is percent-encoded.
type continuation struct {
	num     int
	text    string
	encoded bool
}

func isWordByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_'
}

// rfc2231Name is rfc2231_continuation.match(name): the parameter name and the continuation number of
// "name*", "name*N" and "name*N*".
func rfc2231Name(name string) (base string, num int, ok bool) {
	i := strings.IndexByte(name, '*')
	if i <= 0 {
		return "", 0, false
	}
	for j := range i {
		if !isWordByte(name[j]) {
			return "", 0, false
		}
	}
	rest := name[i+1:]
	if rest == "" {
		return name[:i], -1, true
	}
	digits := strings.TrimSuffix(rest, "*")
	if digits == "" {
		return "", 0, false
	}
	n := 0
	for j := range len(digits) {
		if digits[j] < '0' || digits[j] > '9' {
			return "", 0, false
		}
		n = min(n*10+int(digits[j]-'0'), 1<<30)
	}
	return name[:i], n, true
}

// charsetParam is the charset parameter of a Content-Type value as email.message.Message.get_content_charset
// reads it, without the lower-casing and the ASCII check: the first plain "charset" or, after the plain
// parameters, an RFC 2231 one decoded in the charset it names.
func charsetParam(value string) (charset string, found bool) {
	params := parseParams(value)
	var order []string
	pieces := map[string][]continuation{}
	for _, p := range params[1:] {
		name, val, _ := strings.Cut(p, "=")
		name, val = py.Strip(name), unquoteParam(py.Strip(val))
		if base, num, ok := rfc2231Name(name); ok {
			if _, seen := pieces[base]; !seen {
				order = append(order, base)
			}
			pieces[base] = append(pieces[base], continuation{num: num, text: val, encoded: strings.HasSuffix(name, "*")})
			continue
		}
		if py.Lower(name) == "charset" {
			return val, true
		}
	}
	for _, base := range order {
		if py.Lower(base) != "charset" {
			continue
		}
		parts := pieces[base]
		if slices.ContainsFunc(parts, func(c continuation) bool { return c.num == 0 }) {
			parts = slices.DeleteFunc(parts, func(c continuation) bool { return c.num < 0 })
		} else {
			for i := range parts {
				parts[i].num = max(parts[i].num, 0)
			}
		}
		slices.SortStableFunc(parts, func(a, b continuation) int { return a.num - b.num })
		var joined strings.Builder
		extended := false
		for _, c := range parts {
			text := c.text
			if c.encoded {
				text = unquoteLatin1(text)
				extended = true
			}
			joined.WriteString(text)
		}
		if !extended {
			return joined.String(), true
		}
		fields := strings.SplitN(joined.String(), "'", 3)
		if len(fields) <= 2 {
			return joined.String(), true
		}
		pcharset := fields[0]
		if pcharset == "" {
			pcharset = "us-ascii"
		}
		raw := make([]byte, 0, len(fields[2]))
		for _, r := range fields[2] {
			raw = append(raw, byte(r))
		}
		if decoded, err := mime.DecodeStrict(raw, pcharset); err == nil {
			return decoded, true
		}
		return fields[2], true
	}
	return "", false
}

// unquoteLatin1 is urllib.parse.unquote(s, encoding="latin-1"): each %XX becomes the character with that code.
func unquoteLatin1(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) && isHexByte(s[i+1]) && isHexByte(s[i+2]) {
			v, _ := strconv.ParseUint(s[i+1:i+3], 16, 8)
			b.WriteRune(rune(v))
			i += 2
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func isHexByte(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

// contentCharset is response.headers.get_content_charset() or "utf-8": the lower-cased charset parameter of the
// first Content-Type header. A charset with non-ASCII characters is ignored.
func contentCharset(h headers) string {
	value, ok := h.first("content-type")
	if !ok {
		return "utf-8"
	}
	charset, found := charsetParam(value)
	if !found || !isASCII(charset) {
		return "utf-8"
	}
	if charset = py.Lower(charset); charset != "" {
		return charset
	}
	return "utf-8"
}

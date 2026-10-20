package pyhtml

import "strings"

const maxCodepoint = 0x10FFFF

func Unescape(s string) string {
	if !strings.Contains(s, "&") {
		return s
	}
	rs := []rune(s)
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(rs); {
		if rs[i] != '&' {
			b.WriteRune(rs[i])
			i++
			continue
		}
		n, repl, ok := charref(rs[i+1:])
		if !ok {
			b.WriteRune('&')
			i++
			continue
		}
		b.WriteString(repl)
		i += 1 + n
	}
	return b.String()
}

func isDigit(r rune) bool { return r >= '0' && r <= '9' }

func isHexDigit(r rune) bool {
	return isDigit(r) || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F'
}

func charref(rs []rune) (int, string, bool) {
	if len(rs) == 0 {
		return 0, "", false
	}
	if rs[0] == '#' {
		if n := run(rs[1:], isDigit); n > 0 {
			end := 1 + n
			return withSemicolon(rs, end), numeric(rs[1:end], 10), true
		}
		if len(rs) > 1 && (rs[1] == 'x' || rs[1] == 'X') {
			if n := run(rs[2:], isHexDigit); n > 0 {
				end := 2 + n
				return withSemicolon(rs, end), numeric(rs[2:end], 16), true
			}
		}
		return 0, "", false
	}
	n := 0
	for n < len(rs) && n < 32 && !strings.ContainsRune("\t\n\f <&#;", rs[n]) {
		n++
	}
	if n == 0 {
		return 0, "", false
	}
	end := withSemicolon(rs, n)
	name := string(rs[:end])
	if v, ok := html5[name]; ok {
		return end, v, true
	}
	for x := end - 1; x > 1; x-- {
		if v, ok := html5[string(rs[:x])]; ok {
			return end, v + string(rs[x:end]), true
		}
	}
	return end, "&" + name, true
}

func run(rs []rune, ok func(rune) bool) int {
	n := 0
	for n < len(rs) && ok(rs[n]) {
		n++
	}
	return n
}

func withSemicolon(rs []rune, end int) int {
	if end < len(rs) && rs[end] == ';' {
		return end + 1
	}
	return end
}

func numeric(digits []rune, base int64) string {
	var num int64
	for _, r := range digits {
		var d int64
		switch {
		case isDigit(r):
			d = int64(r - '0')
		case r >= 'a' && r <= 'f':
			d = int64(r-'a') + 10
		default:
			d = int64(r-'A') + 10
		}
		if num <= maxCodepoint {
			num = num*base + d
		}
	}
	if v, ok := invalidCharrefs[num]; ok {
		return v
	}
	if num >= 0xD800 && num <= 0xDFFF || num > maxCodepoint {
		return "\uFFFD"
	}
	if invalidCodepoints[num] {
		return ""
	}
	return string(rune(num))
}

package mime

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

const surrogateBase = 0xDC00

func isSurrogate(r rune) bool { return r >= 0xD800 && r <= 0xDFFF }

func escapeBytes(b []byte) []rune {
	out := make([]rune, len(b))
	for i, c := range b {
		if c < utf8.RuneSelf {
			out[i] = rune(c)
		} else {
			out[i] = surrogateBase + rune(c)
		}
	}
	return out
}

func unescapeBytes(s []rune) []byte {
	out := make([]byte, 0, len(s))
	for _, r := range s {
		switch {
		case r >= surrogateBase+0x80 && r <= surrogateBase+0xFF:
			out = append(out, byte(r-surrogateBase))
		case isSurrogate(r):
			out = utf8.AppendRune(out, utf8.RuneError)
		default:
			out = utf8.AppendRune(out, r)
		}
	}
	return out
}

func hasSurrogates(s []rune) bool {
	for _, r := range s {
		if isSurrogate(r) {
			return true
		}
	}
	return false
}

func sanitize(s []rune) string {
	if !hasSurrogates(s) {
		return string(s)
	}
	return string(decodeUTF8(unescapeBytes(s), errReplace))
}

func isSpace(r rune) bool {
	if r < utf8.RuneSelf {
		return r >= '\t' && r <= '\r' || r >= 0x1C && r <= ' '
	}
	return unicode.Is(pySpace, r)
}

func strip(s []rune) []rune { return rstrip(lstrip(s)) }

func lstrip(s []rune) []rune {
	for len(s) > 0 && isSpace(s[0]) {
		s = s[1:]
	}
	return s
}

func rstrip(s []rune) []rune {
	for len(s) > 0 && isSpace(s[len(s)-1]) {
		s = s[:len(s)-1]
	}
	return s
}

func splitSpace(s []rune) [][]rune {
	var out [][]rune
	i := 0
	for i < len(s) {
		for i < len(s) && isSpace(s[i]) {
			i++
		}
		j := i
		for j < len(s) && !isSpace(s[j]) {
			j++
		}
		if j > i {
			out = append(out, s[i:j])
		}
		i = j
	}
	return out
}

func indexRunes(s, sub []rune) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if hasPrefix(s[i:], sub) {
			return i
		}
	}
	return -1
}

func indexRune(s []rune, r rune) int {
	for i, c := range s {
		if c == r {
			return i
		}
	}
	return -1
}

func countRune(s []rune, r rune) int {
	n := 0
	for _, c := range s {
		if c == r {
			n++
		}
	}
	return n
}

func hasPrefix(s, p []rune) bool {
	if len(p) > len(s) {
		return false
	}
	for i := range p {
		if s[i] != p[i] {
			return false
		}
	}
	return true
}

func hasPrefixStr(s []rune, p string) bool {
	i := 0
	for _, r := range p {
		if i >= len(s) || s[i] != r {
			return false
		}
		i++
	}
	return true
}

func equalStr(s []rune, p string) bool {
	return len(s) == utf8.RuneCountInString(p) && hasPrefixStr(s, p)
}

func inSet(set string, r rune) bool { return r < utf8.RuneSelf && strings.IndexByte(set, byte(r)) >= 0 }

// Lower is Python's str.lower.
func Lower(s string) string {
	for i := range len(s) {
		if s[i] >= utf8.RuneSelf {
			return string(lowerRunes([]rune(s)))
		}
	}
	return strings.ToLower(s)
}

func lowerRunes(s []rune) []rune {
	out := make([]rune, 0, len(s))
	for i, r := range s {
		switch {
		case r < utf8.RuneSelf:
			if r >= 'A' && r <= 'Z' {
				r += 'a' - 'A'
			}
			out = append(out, r)
		case r == 0x3A3:
			if finalSigma(s, i) {
				out = append(out, 0x3C2)
			} else {
				out = append(out, 0x3C3)
			}
		default:
			if lo, ok := pyLower[r]; ok {
				out = append(out, []rune(lo)...)
			} else {
				out = append(out, r)
			}
		}
	}
	return out
}

func finalSigma(s []rune, i int) bool {
	j := i - 1
	for j >= 0 && unicode.Is(pyCaseIgnorable, s[j]) {
		j--
	}
	if j < 0 || !unicode.Is(pyCased, s[j]) {
		return false
	}
	k := i + 1
	for k < len(s) && unicode.Is(pyCaseIgnorable, s[k]) {
		k++
	}
	return k >= len(s) || !unicode.Is(pyCased, s[k])
}

func isDigit(r rune) bool {
	if r < utf8.RuneSelf {
		return r >= '0' && r <= '9'
	}
	return unicode.Is(pyDigit, r)
}

func decimalValue(r rune) (int, bool) {
	if r < utf8.RuneSelf {
		return int(r - '0'), r >= '0' && r <= '9'
	}
	for _, rg := range pyDecimal.R16 {
		if r >= rune(rg.Lo) && r <= rune(rg.Hi) {
			return int(r-rune(rg.Lo)) % 10, true
		}
	}
	for _, rg := range pyDecimal.R32 {
		if r >= rune(rg.Lo) && r <= rune(rg.Hi) {
			return int(r-rune(rg.Lo)) % 10, true
		}
	}
	return 0, false
}

const intLimit = 1 << 40

func pyInt(s []rune) (int64, bool) {
	s = strip(s)
	neg := false
	if len(s) > 0 && (s[0] == '+' || s[0] == '-') {
		neg = s[0] == '-'
		s = s[1:]
	}
	if len(s) == 0 {
		return 0, false
	}
	var n int64
	prevUnderscore := true
	for _, r := range s {
		if r == '_' {
			if prevUnderscore {
				return 0, false
			}
			prevUnderscore = true
			continue
		}
		d, ok := decimalValue(r)
		if !ok {
			return 0, false
		}
		prevUnderscore = false
		if n < intLimit {
			n = n*10 + int64(d)
		}
	}
	if prevUnderscore {
		return 0, false
	}
	if neg {
		n = -n
	}
	return n, true
}

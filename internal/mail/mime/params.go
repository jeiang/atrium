package mime

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

type paramValue struct {
	tuple         bool
	charset, lang string
	hasCharset    bool
	text          string
}

type rawParam struct {
	name  string
	value paramValue
}

func parseParams(s string) []rawParam {
	s = ";" + s
	var plist []string
	start := 0
	for strings.HasPrefix(s[start:], ";") {
		start++
		end := indexFrom(s, ";", start)
		ind, diff := start, 0
		for end > 0 {
			diff += strings.Count(pySlice(s, ind, end), `"`) - strings.Count(pySlice(s, ind, end), `\"`)
			if diff%2 == 0 {
				break
			}
			end, ind = ind, indexFrom(s, ";", end+1)
		}
		if end < 0 {
			end = len(s)
		}
		var f string
		if i := strings.Index(s[start:end], "="); i < 0 {
			f = s[start:end]
		} else {
			i += start
			f = Lower(strings.TrimRightFunc(s[start:i], isSpace)) + "=" + strings.TrimLeftFunc(s[i+1:end], isSpace)
		}
		plist = append(plist, strings.TrimFunc(f, isSpace))
		start = end
	}
	out := make([]rawParam, 0, len(plist))
	for _, p := range plist {
		name, val, ok := strings.Cut(p, "=")
		if !ok {
			out = append(out, rawParam{name: strings.TrimFunc(p, isSpace)})
			continue
		}
		out = append(out, rawParam{name: strings.TrimFunc(name, isSpace), value: paramValue{text: strings.TrimFunc(val, isSpace)}})
	}
	return out
}

func indexFrom(s, sub string, from int) int {
	if from > len(s) {
		return -1
	}
	if i := strings.Index(s[from:], sub); i >= 0 {
		return i + from
	}
	return -1
}

func pySlice(s string, start, end int) string {
	clamp := func(i int) int {
		if i < 0 {
			i += len(s)
		}
		return max(0, min(i, len(s)))
	}
	start, end = clamp(start), clamp(end)
	if start >= end {
		return ""
	}
	return s[start:end]
}

func unquote(s string) string {
	if utf8.RuneCountInString(s) > 1 {
		if strings.HasPrefix(s, `"`) && strings.HasSuffix(s, `"`) {
			s = s[1 : len(s)-1]
			return strings.ReplaceAll(strings.ReplaceAll(s, `\\`, `\`), `\"`, `"`)
		}
		if strings.HasPrefix(s, "<") && strings.HasSuffix(s, ">") {
			return s[1 : len(s)-1]
		}
	}
	return s
}

func quote(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), `"`, `\"`)
}

func rfc2231Continuation(name string) (string, int, bool, bool) {
	i := 0
	for i < len(name) && (name[i] == '_' || name[i] >= '0' && name[i] <= '9' || name[i] >= 'a' && name[i] <= 'z' || name[i] >= 'A' && name[i] <= 'Z') {
		i++
	}
	if i == 0 || i >= len(name) || name[i] != '*' {
		return "", 0, false, false
	}
	base, rest := name[:i], name[i+1:]
	if rest == "" {
		return base, 0, false, true
	}
	j := 0
	for j < len(rest) && rest[j] >= '0' && rest[j] <= '9' {
		j++
	}
	if j == 0 || !(j == len(rest) || j == len(rest)-1 && rest[j] == '*') {
		return "", 0, false, false
	}
	num := 0
	for _, c := range rest[:j] {
		if num < intLimit {
			num = num*10 + int(c-'0')
		}
	}
	return base, num, true, true
}

func decodeParams(params []rawParam) []rawParam {
	out := []rawParam{params[0]}
	type cont struct {
		num     int
		hasNum  bool
		value   string
		encoded bool
	}
	var order []string
	conts := map[string][]cont{}
	for _, p := range params[1:] {
		encoded := strings.HasSuffix(p.name, "*")
		value := unquote(p.value.text)
		if base, num, hasNum, ok := rfc2231Continuation(p.name); ok {
			if _, seen := conts[base]; !seen {
				order = append(order, base)
			}
			conts[base] = append(conts[base], cont{num, hasNum, value, encoded})
		} else {
			out = append(out, rawParam{name: p.name, value: paramValue{text: `"` + quote(value) + `"`}})
		}
	}
	for _, name := range order {
		cs := conts[name]
		hasZero := false
		for _, c := range cs {
			if c.hasNum && c.num == 0 {
				hasZero = true
			}
		}
		var kept []cont
		for _, c := range cs {
			if hasZero && !c.hasNum {
				continue
			}
			kept = append(kept, c)
		}
		sort.SliceStable(kept, func(i, j int) bool { return kept[i].num < kept[j].num })
		var b strings.Builder
		extended := false
		for _, c := range kept {
			s := c.value
			if c.encoded {
				s = percentUnquoteLatin1(s)
				extended = true
			}
			b.WriteString(s)
		}
		value := quote(b.String())
		if extended {
			parts := strings.SplitN(value, "'", 3)
			if len(parts) <= 2 {
				out = append(out, rawParam{name: name, value: paramValue{tuple: true, text: `"` + value + `"`}})
			} else {
				out = append(out, rawParam{name: name, value: paramValue{tuple: true, charset: parts[0], hasCharset: true, lang: parts[1], text: `"` + parts[2] + `"`}})
			}
		} else {
			out = append(out, rawParam{name: name, value: paramValue{text: `"` + value + `"`}})
		}
	}
	return out
}

func percentUnquoteLatin1(s string) string {
	if !strings.Contains(s, "%") {
		return s
	}
	var b strings.Builder
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		if rs[i] == '%' && i+2 < len(rs) && rs[i+1] < 0x80 && rs[i+2] < 0x80 && isHex(byte(rs[i+1])) && isHex(byte(rs[i+2])) {
			b.WriteRune(rune(hexValue(byte(rs[i+1]))<<4 | hexValue(byte(rs[i+2]))))
			i += 2
			continue
		}
		b.WriteRune(rs[i])
	}
	return b.String()
}

func (v paramValue) collapse() string {
	if !v.tuple {
		return unquote(v.text)
	}
	charset := v.charset
	if !v.hasCharset {
		charset = "us-ascii"
	}
	var raw []byte
	for _, r := range v.text {
		switch {
		case r < 0x100:
			raw = append(raw, byte(r))
		case r < 0x10000:
			raw = append(raw, fmt.Sprintf(`\u%04x`, r)...)
		default:
			raw = append(raw, fmt.Sprintf(`\U%08x`, r)...)
		}
	}
	out, err := decodeBytes(raw, charset, errReplace)
	if err != nil {
		return unquote(v.text)
	}
	return string(out)
}

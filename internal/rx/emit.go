package rx

import (
	"fmt"
	"slices"
	"strings"
	"unicode"
)

// Python's \w, \d and \s for str patterns; \w and \d are exactly these Unicode categories in Python 3.14.
const (
	uniWord  = `\p{L}\p{N}_`
	uniDigit = `\p{Nd}`
	uniSpace = `\u0009-\u000d\u001c-\u0020\u0085\u00a0\u1680\u2000-\u200a\u2028\u2029\u202f\u205f\u3000`

	asciiWord  = `0-9A-Z_a-z`
	asciiDigit = `0-9`
	asciiSpace = `\u0009-\u000d\u0020`
)

type ciWant uint8

const (
	ciAny ciWant = iota
	ciOn
	ciOff
)

func asciiMode(f Flags) bool { return f&(ASCII|Bytes) != 0 }

func wordClass(f Flags) string {
	if asciiMode(f) {
		return "[" + asciiWord + "]"
	}
	return "[" + uniWord + "]"
}

func want(n *node) (ciWant, bool) {
	switch n.kind {
	case nLiteral, nClass:
		if n.flags&IgnoreCase != 0 && !asciiMode(n.flags) {
			return ciOn, true
		}
		return ciOff, true
	case nBackref:
		if n.flags&IgnoreCase != 0 {
			return ciOn, true
		}
		return ciOff, true
	case nCategory, nAt, nAny:
		if asciiMode(n.flags) {
			return ciOff, true
		}
		return ciAny, true
	case nFail:
		return ciAny, true
	}
	return ciAny, false
}

type emitter struct {
	b strings.Builder
}

func emitPattern(root *node, ci bool) string {
	e := &emitter{}
	e.emit(root, ci)
	return e.b.String()
}

func (e *emitter) emit(n *node, ci bool) {
	if w, leaf := want(n); leaf {
		if w == ciAny || (w == ciOn) == ci {
			e.leaf(n)
			return
		}
		e.wrapCI(w == ciOn, func() { e.leaf(n) })
		return
	}
	switch n.kind {
	case nConcat:
		for i := 0; i < len(n.subs); {
			w, leaf := want(n.subs[i])
			if !leaf || w == ciAny || (w == ciOn) == ci {
				e.emit(n.subs[i], ci)
				i++
				continue
			}
			j := i
			for j < len(n.subs) {
				wj, lj := want(n.subs[j])
				if !lj || (wj != w && wj != ciAny) {
					break
				}
				j++
			}
			run := n.subs[i:j]
			e.wrapCI(w == ciOn, func() {
				for _, s := range run {
					e.leaf(s)
				}
			})
			i = j
		}
	case nAlt:
		e.b.WriteString("(?:")
		for i, s := range n.subs {
			if i > 0 {
				e.b.WriteByte('|')
			}
			e.emit(s, ci)
		}
		e.b.WriteByte(')')
	case nGroup:
		if n.group > 0 {
			e.b.WriteByte('(')
		} else {
			e.b.WriteString("(?:")
		}
		e.emit(n.subs[0], ci)
		e.b.WriteByte(')')
	case nAtomic:
		e.b.WriteString("(?>")
		e.emit(n.subs[0], ci)
		e.b.WriteByte(')')
	case nLook:
		e.b.WriteString("(?")
		if n.behind {
			e.b.WriteByte('<')
		}
		if n.negate {
			e.b.WriteByte('!')
		} else {
			e.b.WriteByte('=')
		}
		e.emit(n.subs[0], ci)
		e.b.WriteByte(')')
	case nCond:
		fmt.Fprintf(&e.b, "(?(%d)", n.group)
		e.emit(n.subs[0], ci)
		// regexp2 skips a match when a leading (?(N)yes) has no "no" branch and group N did not participate.
		e.b.WriteByte('|')
		if n.hasNo {
			e.emit(n.no, ci)
		}
		e.b.WriteByte(')')
	case nRepeat:
		if n.repeat == possessive {
			e.b.WriteString("(?>")
		}
		e.b.WriteString("(?:")
		e.emit(n.subs[0], ci)
		e.b.WriteByte(')')
		switch {
		case n.min == 0 && n.max == maxRepeat:
			e.b.WriteByte('*')
		case n.min == 1 && n.max == maxRepeat:
			e.b.WriteByte('+')
		case n.min == 0 && n.max == 1:
			e.b.WriteByte('?')
		case n.max == maxRepeat:
			fmt.Fprintf(&e.b, "{%d,}", n.min)
		case n.min == n.max:
			fmt.Fprintf(&e.b, "{%d}", n.min)
		default:
			fmt.Fprintf(&e.b, "{%d,%d}", n.min, n.max)
		}
		if n.repeat == lazy {
			e.b.WriteByte('?')
		}
		if n.repeat == possessive {
			e.b.WriteByte(')')
		}
	}
}

func (e *emitter) wrapCI(on bool, body func()) {
	if on {
		e.b.WriteString("(?i:")
	} else {
		e.b.WriteString("(?-i:")
	}
	body()
	e.b.WriteByte(')')
}

func (e *emitter) leaf(n *node) {
	switch n.kind {
	case nLiteral:
		e.literal(n)
	case nClass:
		e.class(n.items, n.negate, n.flags)
	case nCategory:
		e.class([]classItem{{cat: n.cat, isCat: true}}, false, n.flags)
	case nAny:
		if n.flags&DotAll != 0 {
			e.b.WriteString(`[\s\S]`)
		} else {
			e.b.WriteString(`[^\n]`)
		}
	case nAt:
		e.at(n)
	case nBackref:
		fmt.Fprintf(&e.b, `(?:\%d)`, n.group)
	case nFail:
		e.b.WriteString("(?!)")
	}
}

func (e *emitter) at(n *node) {
	switch n.at {
	case atBeginning:
		if n.flags&Multiline != 0 {
			e.b.WriteString(`(?<![^\n])`)
		} else {
			e.b.WriteString(`\A`)
		}
	case atBeginningString:
		e.b.WriteString(`\A`)
	case atEnd:
		if n.flags&Multiline != 0 {
			e.b.WriteString(`(?=\n|\z)`)
		} else {
			e.b.WriteString(`(?=\n?\z)`)
		}
	case atEndString:
		e.b.WriteString(`\z`)
	case atBoundary:
		w := wordClass(n.flags)
		fmt.Fprintf(&e.b, `(?:(?<=%[1]s)(?!%[1]s)|(?<!%[1]s)(?=%[1]s))`, w)
	case atNonBoundary:
		w := wordClass(n.flags)
		fmt.Fprintf(&e.b, `(?:(?<=%[1]s)(?=%[1]s)|(?<!%[1]s)(?!%[1]s))`, w)
	}
}

func (e *emitter) literal(n *node) {
	r := n.r
	if n.flags&IgnoreCase == 0 {
		e.b.WriteString(escapeRune(r))
		return
	}
	if asciiMode(n.flags) {
		if o, ok := asciiOtherCase(r); ok {
			e.b.WriteString("[" + escapeRune(r) + escapeRune(o) + "]")
			return
		}
		e.b.WriteString(escapeRune(r))
		return
	}
	if fixes, ok := caseFixes[unicode.ToLower(r)]; ok {
		e.b.WriteString("[" + escapeRune(unicode.ToLower(r)))
		for _, f := range fixes {
			e.b.WriteString(escapeRune(f))
		}
		e.b.WriteString("]")
		return
	}
	e.b.WriteString(escapeRune(r))
}

func asciiOtherCase(r rune) (rune, bool) {
	switch {
	case r >= 'a' && r <= 'z':
		return r - 'a' + 'A', true
	case r >= 'A' && r <= 'Z':
		return r - 'A' + 'a', true
	}
	return 0, false
}

// class emits a Python character set. regexp2 cannot union a complemented category (\W, \S, \D) with other
// members of one set (CharIn stops at the first category holding the character, so [\P{Nd}\p{N}] rejects "1"),
// so a set holding one becomes an alternation of sets, negated through a lookahead.
func (e *emitter) class(items []classItem, negate bool, flags Flags) {
	var body strings.Builder
	var complements []string
	ascii := asciiMode(flags)
	ranges := make([][2]rune, 0, len(items))
	for _, it := range items {
		if !it.isCat {
			ranges = append(ranges, [2]rune{it.lo, it.hi})
			continue
		}
		switch it.cat {
		case catWord:
			body.WriteString(pick(ascii, asciiWord, uniWord))
		case catDigit:
			body.WriteString(pick(ascii, asciiDigit, uniDigit))
		case catSpace:
			body.WriteString(pick(ascii, asciiSpace, uniSpace))
		case catNotDigit:
			complements = append(complements, pick(ascii, asciiDigit, uniDigit))
		case catNotWord:
			complements = append(complements, pick(ascii, asciiWord, uniWord))
		case catNotSpace:
			complements = append(complements, pick(ascii, asciiSpace, uniSpace))
		}
	}
	if flags&IgnoreCase != 0 {
		if ascii {
			ranges = addASCIICase(ranges)
		} else {
			ranges = addCaseFixes(ranges)
		}
	}
	for _, r := range ranges {
		body.WriteString(escapeRune(r[0]))
		if r[1] != r[0] {
			body.WriteByte('-')
			body.WriteString(escapeRune(r[1]))
		}
	}
	if len(complements) == 0 {
		if body.Len() == 0 {
			if negate {
				e.b.WriteString(`[\s\S]`)
			} else {
				e.b.WriteString(`(?!)`)
			}
			return
		}
		e.b.WriteByte('[')
		if negate {
			e.b.WriteByte('^')
		}
		e.b.WriteString(body.String())
		e.b.WriteByte(']')
		return
	}
	if body.Len() == 0 && len(complements) == 1 {
		if negate {
			e.b.WriteString("[" + complements[0] + "]")
		} else {
			e.b.WriteString("[^" + complements[0] + "]")
		}
		return
	}
	alts := make([]string, 0, len(complements)+1)
	if body.Len() > 0 {
		alts = append(alts, "["+body.String()+"]")
	}
	for _, c := range complements {
		alts = append(alts, "[^"+c+"]")
	}
	union := "(?:" + strings.Join(alts, "|") + ")"
	if negate {
		e.b.WriteString(`(?!` + union + `)[\s\S]`)
		return
	}
	e.b.WriteString(union)
}

func pick(ascii bool, a, u string) string {
	if ascii {
		return a
	}
	return u
}

func addASCIICase(ranges [][2]rune) [][2]rune {
	out := slices.Clone(ranges)
	for _, r := range ranges {
		if lo, hi := max(r[0], 'A'), min(r[1], 'Z'); lo <= hi {
			out = append(out, [2]rune{lo + 32, hi + 32})
		}
		if lo, hi := max(r[0], 'a'), min(r[1], 'z'); lo <= hi {
			out = append(out, [2]rune{lo - 32, hi - 32})
		}
	}
	return out
}

func inRanges(ranges [][2]rune, c rune) bool {
	for _, r := range ranges {
		if c >= r[0] && c <= r[1] {
			return true
		}
	}
	return false
}

// addCaseFixes adds the characters Python's re treats as case-equivalent beyond simple lowercasing (re._casefix).
func addCaseFixes(ranges [][2]rune) [][2]rune {
	out := ranges
	for _, k := range caseFixKeys {
		fixes := caseFixes[k]
		hit := false
		for c := k; ; {
			if inRanges(ranges, c) {
				hit = true
				break
			}
			if c = unicode.SimpleFold(c); c == k {
				break
			}
		}
		if !hit {
			continue
		}
		out = append(out, [2]rune{k, k})
		for _, f := range fixes {
			out = append(out, [2]rune{f, f})
		}
	}
	return out
}

func escapeRune(r rune) string {
	switch {
	case r >= '0' && r <= '9', r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '_':
		return string(r)
	case r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0xffff):
		return `\u` + fmt.Sprintf("%04x", r)
	case r < 0x80:
		return `\` + string(r)
	}
	return string(r)
}

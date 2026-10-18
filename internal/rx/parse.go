package rx

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"unicode"
)

// Error is a pattern or replacement template Python's re module rejects. Msg follows Python's wording where the
// parser mirrors it; only "compiles or not" is guaranteed to agree with Python.
type Error struct {
	Msg     string
	Pattern string
	Pos     int
}

func (e *Error) Error() string {
	if e.Pos < 0 {
		return e.Msg
	}
	msg := fmt.Sprintf("%s at position %d", e.Msg, e.Pos)
	src := []rune(e.Pattern)
	if !slices.Contains(src, '\n') {
		return msg
	}
	before := src[:min(e.Pos, len(src))]
	line := 1
	col := e.Pos + 1
	for i, r := range before {
		if r == '\n' {
			line++
			col = e.Pos - i
		}
	}
	return fmt.Sprintf("%s (line %d, column %d)", msg, line, col)
}

const (
	maxRepeat = math.MaxUint32
	maxGroups = math.MaxInt32 / 2
	maxCode   = math.MaxUint32
	maxWidth  = math.MaxUint64
)

type nodeKind uint8

const (
	nLiteral nodeKind = iota
	nAny
	nClass
	nCategory
	nAt
	nGroup
	nLook
	nBackref
	nCond
	nAtomic
	nRepeat
	nAlt
	nConcat
	nFail
)

type atKind uint8

const (
	atBeginning atKind = iota
	atBeginningString
	atEnd
	atEndString
	atBoundary
	atNonBoundary
)

type category uint8

const (
	catDigit category = iota
	catNotDigit
	catSpace
	catNotSpace
	catWord
	catNotWord
)

type repeatKind uint8

const (
	greedy repeatKind = iota
	lazy
	possessive
)

type classItem struct {
	lo, hi rune
	cat    category
	isCat  bool
}

type node struct {
	kind     nodeKind
	r        rune
	flags    Flags
	negate   bool
	items    []classItem
	cat      category
	at       atKind
	group    int
	behind   bool
	min, max uint64
	repeat   repeatKind
	subs     []*node
	no       *node
	hasNo    bool
}

type width struct{ lo, hi uint64 }

func satAdd(a, b uint64) uint64 {
	if a > maxWidth-b {
		return maxWidth
	}
	return a + b
}

func satMul(a, b uint64) uint64 {
	if a == 0 || b == 0 {
		return 0
	}
	if a > maxWidth/b {
		return maxWidth
	}
	return a * b
}

type parser struct {
	src         []rune
	pattern     string
	text        bool
	index       int
	next        string
	hasNext     bool
	flags       Flags
	groupNames  map[string]int
	groupWidths []*width
	lookbehind  int
	grouprefs   []groupref
}

type groupref struct{ group, pos int }

func (p *parser) advance() error {
	i := p.index
	if i >= len(p.src) {
		p.hasNext = false
		p.next = ""
		return nil
	}
	ch := string(p.src[i])
	if p.src[i] == '\\' {
		i++
		if i >= len(p.src) {
			return &Error{Msg: "bad escape (end of pattern)", Pattern: p.pattern, Pos: len(p.src) - 1}
		}
		ch += string(p.src[i])
	}
	p.index = i + 1
	p.next = ch
	p.hasNext = true
	return nil
}

func (p *parser) match(s string) (bool, error) {
	if p.hasNext && p.next == s {
		return true, p.advance()
	}
	return false, nil
}

func (p *parser) get() (string, bool, error) {
	this, ok := p.next, p.hasNext
	return this, ok, p.advance()
}

func (p *parser) nextIn(set string) bool {
	if !p.hasNext {
		return false
	}
	r := []rune(p.next)
	if len(r) != 1 {
		return false
	}
	for _, c := range set {
		if c == r[0] {
			return true
		}
	}
	return false
}

func (p *parser) getwhile(n int, set string) (string, error) {
	out := ""
	for range n {
		if !p.nextIn(set) {
			break
		}
		out += p.next
		if err := p.advance(); err != nil {
			return "", err
		}
	}
	return out, nil
}

func (p *parser) getuntil(term, name string) (string, error) {
	out := ""
	for {
		c, ok, err := p.get()
		if err != nil {
			return "", err
		}
		if !ok {
			if out == "" {
				return "", p.errorf(0, "missing %s", name)
			}
			return "", p.errorf(runeLen(out), "missing %s, unterminated name", term)
		}
		if c == term {
			if out == "" {
				return "", p.errorf(1, "missing %s", name)
			}
			return out, nil
		}
		out += c
	}
}

func (p *parser) tell() int {
	return p.index - runeLen(p.next)
}

func (p *parser) seek(i int) error {
	p.index = i
	return p.advance()
}

func (p *parser) errorf(offset int, format string, args ...any) *Error {
	return &Error{Msg: fmt.Sprintf(format, args...), Pattern: p.pattern, Pos: p.tell() - offset}
}

func (p *parser) checkGroupName(name string, offset int) error {
	if !p.text && !isASCII(name) {
		return p.errorf(runeLen(name)+offset, "bad character in group name %s", pyASCIIRepr(name))
	}
	if !isIdentifier(name) {
		return p.errorf(runeLen(name)+offset, "bad character in group name %s", pyRepr(name))
	}
	return nil
}

func (p *parser) groups() int { return len(p.groupWidths) }

func (p *parser) openGroup(name string, named bool) (int, error) {
	gid := p.groups()
	p.groupWidths = append(p.groupWidths, nil)
	if p.groups() > maxGroups {
		return 0, &Error{Msg: "too many groups", Pattern: p.pattern, Pos: -1}
	}
	if named {
		if ogid, ok := p.groupNames[name]; ok {
			return 0, &Error{Msg: fmt.Sprintf("redefinition of group name %s as group %d; was group %d", pyRepr(name), gid, ogid), Pattern: p.pattern, Pos: -1}
		}
		p.groupNames[name] = gid
	}
	return gid, nil
}

func (p *parser) checkGroup(gid int) bool {
	return gid < p.groups() && p.groupWidths[gid] != nil
}

func (p *parser) checkLookbehindGroup(gid int) error {
	if p.lookbehind >= 0 {
		if !p.checkGroup(gid) {
			return p.errorf(0, "cannot refer to an open group")
		}
		if gid >= p.lookbehind {
			return p.errorf(0, "cannot refer to group defined in the same lookbehind subpattern")
		}
	}
	return nil
}

const (
	digits    = "0123456789"
	octDigits = "01234567"
	hexDigits = "0123456789abcdefABCDEF"
	special   = ".\\[{()*+?^$|"
	repeats   = "*+?{"
)

var simpleEscapes = map[rune]rune{'a': '\a', 'b': '\b', 'f': '\f', 'n': '\n', 'r': '\r', 't': '\t', 'v': '\v', '\\': '\\'}

var categoryEscapes = map[rune]category{'d': catDigit, 'D': catNotDigit, 's': catSpace, 'S': catNotSpace, 'w': catWord, 'W': catNotWord}

var atEscapes = map[rune]atKind{'A': atBeginningString, 'b': atBoundary, 'B': atNonBoundary, 'z': atEndString, 'Z': atEndString}

func isASCIILetter(c rune) bool { return c < 128 && unicode.IsLetter(c) }

// numericEscape handles \x, \u, \U and \N, shared by both escape contexts. ok is false when esc is none of them.
func (p *parser) numericEscape(esc string) (rune, bool, error) {
	c := []rune(esc)[1]
	switch {
	case c == 'x':
		more, err := p.getwhile(2, hexDigits)
		if err != nil {
			return 0, false, err
		}
		esc += more
		if runeLen(esc) != 4 {
			return 0, false, p.errorf(runeLen(esc), "incomplete escape %s", esc)
		}
		v, _ := strconv.ParseUint(esc[2:], 16, 32)
		return rune(v), true, nil
	case c == 'u' && p.text:
		more, err := p.getwhile(4, hexDigits)
		if err != nil {
			return 0, false, err
		}
		esc += more
		if runeLen(esc) != 6 {
			return 0, false, p.errorf(runeLen(esc), "incomplete escape %s", esc)
		}
		v, _ := strconv.ParseUint(esc[2:], 16, 32)
		return rune(v), true, nil
	case c == 'U' && p.text:
		more, err := p.getwhile(8, hexDigits)
		if err != nil {
			return 0, false, err
		}
		esc += more
		if runeLen(esc) != 10 {
			return 0, false, p.errorf(runeLen(esc), "incomplete escape %s", esc)
		}
		v, _ := strconv.ParseUint(esc[2:], 16, 32)
		if v > unicode.MaxRune {
			return 0, false, p.errorf(runeLen(esc), "bad escape %s", esc)
		}
		return rune(v), true, nil
	case c == 'N' && p.text:
		ok, err := p.match("{")
		if err != nil {
			return 0, false, err
		}
		if !ok {
			return 0, false, p.errorf(0, "missing {")
		}
		name, err := p.getuntil("}", "character name")
		if err != nil {
			return 0, false, err
		}
		return 0, false, p.errorf(runeLen(name)+4, "named Unicode escape \\N{%s} is not supported", name)
	}
	return 0, false, nil
}

func (p *parser) classEscape(esc string) (classItem, error) {
	c := []rune(esc)[1]
	if v, ok := simpleEscapes[c]; ok {
		return classItem{lo: v, hi: v}, nil
	}
	if cat, ok := categoryEscapes[c]; ok {
		return classItem{cat: cat, isCat: true}, nil
	}
	if v, ok, err := p.numericEscape(esc); err != nil || ok {
		return classItem{lo: v, hi: v}, err
	}
	switch {
	case c >= '0' && c <= '7':
		more, err := p.getwhile(2, octDigits)
		if err != nil {
			return classItem{}, err
		}
		esc += more
		v, _ := strconv.ParseUint(esc[1:], 8, 32)
		if v > 0o377 {
			return classItem{}, p.errorf(runeLen(esc), "octal escape value %s outside of range 0-0o377", esc)
		}
		return classItem{lo: rune(v), hi: rune(v)}, nil
	case c == '8' || c == '9':
		return classItem{}, p.errorf(runeLen(esc), "bad escape %s", esc)
	case isASCIILetter(c):
		return classItem{}, p.errorf(runeLen(esc), "bad escape %s", esc)
	}
	return classItem{lo: c, hi: c}, nil
}

func (p *parser) escape(esc string, flags Flags) (*node, error) {
	c := []rune(esc)[1]
	if at, ok := atEscapes[c]; ok {
		return &node{kind: nAt, at: at, flags: flags}, nil
	}
	if cat, ok := categoryEscapes[c]; ok {
		return &node{kind: nCategory, cat: cat, flags: flags}, nil
	}
	if v, ok := simpleEscapes[c]; ok {
		return &node{kind: nLiteral, r: v, flags: flags}, nil
	}
	if v, ok, err := p.numericEscape(esc); err != nil || ok {
		return &node{kind: nLiteral, r: v, flags: flags}, err
	}
	switch {
	case c == '0':
		more, err := p.getwhile(2, octDigits)
		if err != nil {
			return nil, err
		}
		v, _ := strconv.ParseUint(esc[1:]+more, 8, 32)
		return &node{kind: nLiteral, r: rune(v), flags: flags}, nil
	case c >= '1' && c <= '9':
		if p.nextIn(digits) {
			d, _, err := p.get()
			if err != nil {
				return nil, err
			}
			esc += d
			r := []rune(esc)
			if r[1] <= '7' && r[2] <= '7' && p.nextIn(octDigits) {
				d, _, err := p.get()
				if err != nil {
					return nil, err
				}
				esc += d
				v, _ := strconv.ParseUint(esc[1:], 8, 32)
				if v > 0o377 {
					return nil, p.errorf(runeLen(esc), "octal escape value %s outside of range 0-0o377", esc)
				}
				return &node{kind: nLiteral, r: rune(v), flags: flags}, nil
			}
		}
		group, _ := strconv.Atoi(esc[1:])
		if group < p.groups() {
			if !p.checkGroup(group) {
				return nil, p.errorf(runeLen(esc), "cannot refer to an open group")
			}
			if err := p.checkLookbehindGroup(group); err != nil {
				return nil, err
			}
			return &node{kind: nBackref, group: group, flags: flags}, nil
		}
		return nil, p.errorf(runeLen(esc)-1, "invalid group reference %d", group)
	case isASCIILetter(c):
		return nil, p.errorf(runeLen(esc), "bad escape %s", esc)
	}
	return &node{kind: nLiteral, r: c, flags: flags}, nil
}

func (p *parser) parseSub(flags Flags, nested int) (*node, error) {
	var items []*node
	verbose := flags&Verbose != 0
	for {
		item, err := p.parseSeq(flags, verbose, nested+1, nested == 0 && len(items) == 0)
		if err != nil {
			return nil, err
		}
		if nested == 0 && len(items) == 0 {
			flags = p.flags
		}
		items = append(items, item)
		ok, err := p.match("|")
		if err != nil {
			return nil, err
		}
		if !ok {
			break
		}
		if nested == 0 {
			verbose = p.flags&Verbose != 0
		}
	}
	if len(items) == 1 {
		return items[0], nil
	}
	return &node{kind: nAlt, subs: items}, nil
}

func (p *parser) parseSeq(flags Flags, verbose bool, nested int, first bool) (*node, error) {
	seq := &node{kind: nConcat}
	for p.hasNext && p.next != "|" && p.next != ")" {
		this, _, err := p.get()
		if err != nil {
			return nil, err
		}
		if verbose {
			if this == " " || this == "\t" || this == "\n" || this == "\r" || this == "\v" || this == "\f" {
				continue
			}
			if this == "#" {
				for {
					c, ok, err := p.get()
					if err != nil {
						return nil, err
					}
					if !ok || c == "\n" {
						break
					}
				}
				continue
			}
		}
		r := []rune(this)
		switch {
		case r[0] == '\\':
			n, err := p.escape(this, flags)
			if err != nil {
				return nil, err
			}
			seq.subs = append(seq.subs, n)
		case !containsRune(special, r[0]):
			seq.subs = append(seq.subs, &node{kind: nLiteral, r: r[0], flags: flags})
		case this == "[":
			n, err := p.parseClass(flags)
			if err != nil {
				return nil, err
			}
			seq.subs = append(seq.subs, n)
		case containsRune(repeats, r[0]):
			if err := p.parseRepeat(seq, this, flags); err != nil {
				return nil, err
			}
		case this == ".":
			seq.subs = append(seq.subs, &node{kind: nAny, flags: flags})
		case this == "(":
			n, err := p.parseGroup(seq, flags, verbose, nested, first)
			if err != nil {
				return nil, err
			}
			if n == nil {
				if p.flags != flags && first && len(seq.subs) == 0 {
					flags = p.flags
					verbose = flags&Verbose != 0
				}
				continue
			}
			seq.subs = append(seq.subs, n)
		case this == "^":
			seq.subs = append(seq.subs, &node{kind: nAt, at: atBeginning, flags: flags})
		case this == "$":
			seq.subs = append(seq.subs, &node{kind: nAt, at: atEnd, flags: flags})
		}
	}
	return seq, nil
}

func (p *parser) parseClass(flags Flags) (*node, error) {
	here := p.tell() - 1
	var items []classItem
	negate, err := p.match("^")
	if err != nil {
		return nil, err
	}
	for {
		this, ok, err := p.get()
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, p.errorf(p.tell()-here, "unterminated character set")
		}
		if this == "]" && len(items) > 0 {
			break
		}
		var code1 classItem
		if []rune(this)[0] == '\\' {
			if code1, err = p.classEscape(this); err != nil {
				return nil, err
			}
		} else {
			c := []rune(this)[0]
			code1 = classItem{lo: c, hi: c}
		}
		dash, err := p.match("-")
		if err != nil {
			return nil, err
		}
		if !dash {
			items = append(items, code1)
			continue
		}
		that, ok, err := p.get()
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, p.errorf(p.tell()-here, "unterminated character set")
		}
		if that == "]" {
			items = append(items, code1, classItem{lo: '-', hi: '-'})
			break
		}
		var code2 classItem
		if []rune(that)[0] == '\\' {
			if code2, err = p.classEscape(that); err != nil {
				return nil, err
			}
		} else {
			c := []rune(that)[0]
			code2 = classItem{lo: c, hi: c}
		}
		if code1.isCat || code2.isCat || code2.lo < code1.lo {
			return nil, p.errorf(runeLen(this)+1+runeLen(that), "bad character range %s-%s", this, that)
		}
		items = append(items, classItem{lo: code1.lo, hi: code2.lo})
	}
	return &node{kind: nClass, negate: negate, items: items, flags: flags}, nil
}

func (p *parser) parseRepeat(seq *node, this string, flags Flags) error {
	here := p.tell()
	var lo, hi uint64
	switch this {
	case "?":
		lo, hi = 0, 1
	case "*":
		lo, hi = 0, maxRepeat
	case "+":
		lo, hi = 1, maxRepeat
	case "{":
		if p.hasNext && p.next == "}" {
			seq.subs = append(seq.subs, &node{kind: nLiteral, r: '{', flags: flags})
			return nil
		}
		lo, hi = 0, maxRepeat
		var los, his string
		for p.nextIn(digits) {
			d, _, err := p.get()
			if err != nil {
				return err
			}
			los += d
		}
		comma, err := p.match(",")
		if err != nil {
			return err
		}
		if comma {
			for p.nextIn(digits) {
				d, _, err := p.get()
				if err != nil {
					return err
				}
				his += d
			}
		} else {
			his = los
		}
		closed, err := p.match("}")
		if err != nil {
			return err
		}
		if !closed {
			seq.subs = append(seq.subs, &node{kind: nLiteral, r: '{', flags: flags})
			return p.seek(here)
		}
		if los != "" {
			v, err := strconv.ParseUint(los, 10, 64)
			if err != nil || v >= maxRepeat {
				return &Error{Msg: "the repetition number is too large", Pattern: p.pattern, Pos: -1}
			}
			lo = v
		}
		if his != "" {
			v, err := strconv.ParseUint(his, 10, 64)
			if err != nil || v >= maxRepeat {
				return &Error{Msg: "the repetition number is too large", Pattern: p.pattern, Pos: -1}
			}
			hi = v
			if hi < lo {
				return p.errorf(p.tell()-here, "min repeat greater than max repeat")
			}
		}
	}
	if len(seq.subs) == 0 || seq.subs[len(seq.subs)-1].kind == nAt {
		return p.errorf(p.tell()-here+runeLen(this), "nothing to repeat")
	}
	item := seq.subs[len(seq.subs)-1]
	if item.kind == nRepeat {
		return p.errorf(p.tell()-here+runeLen(this), "multiple repeat")
	}
	kind := greedy
	if ok, err := p.match("?"); err != nil {
		return err
	} else if ok {
		kind = lazy
	} else if ok, err := p.match("+"); err != nil {
		return err
	} else if ok {
		kind = possessive
	}
	seq.subs[len(seq.subs)-1] = &node{kind: nRepeat, min: lo, max: hi, repeat: kind, subs: []*node{item}, flags: flags}
	return nil
}

// parseGroup parses everything after "(". It returns nil for constructs that add no node (comments, global flags).
func (p *parser) parseGroup(seq *node, flags Flags, verbose bool, nested int, first bool) (*node, error) {
	start := p.tell() - 1
	capture := true
	atomic := false
	name := ""
	named := false
	var add, del Flags
	q, err := p.match("?")
	if err != nil {
		return nil, err
	}
	if q {
		char, ok, err := p.get()
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, p.errorf(0, "unexpected end of pattern")
		}
		switch {
		case char == "P":
			if ok, err := p.match("<"); err != nil {
				return nil, err
			} else if ok {
				if name, err = p.getuntil(">", "group name"); err != nil {
					return nil, err
				}
				if err := p.checkGroupName(name, 1); err != nil {
					return nil, err
				}
				named = true
			} else if ok, err := p.match("="); err != nil {
				return nil, err
			} else if ok {
				name, err := p.getuntil(")", "group name")
				if err != nil {
					return nil, err
				}
				if err := p.checkGroupName(name, 1); err != nil {
					return nil, err
				}
				gid, ok := p.groupNames[name]
				if !ok {
					return nil, p.errorf(runeLen(name)+1, "unknown group name %s", pyRepr(name))
				}
				if !p.checkGroup(gid) {
					return nil, p.errorf(runeLen(name)+1, "cannot refer to an open group")
				}
				if err := p.checkLookbehindGroup(gid); err != nil {
					return nil, err
				}
				return &node{kind: nBackref, group: gid, flags: flags}, nil
			} else {
				c, ok, err := p.get()
				if err != nil {
					return nil, err
				}
				if !ok {
					return nil, p.errorf(0, "unexpected end of pattern")
				}
				return nil, p.errorf(runeLen(c)+2, "unknown extension ?P%s", c)
			}
		case char == ":":
			capture = false
		case char == "#":
			for {
				if !p.hasNext {
					return nil, p.errorf(p.tell()-start, "missing ), unterminated comment")
				}
				c, _, err := p.get()
				if err != nil {
					return nil, err
				}
				if c == ")" {
					return nil, nil
				}
			}
		case char == "=" || char == "!" || char == "<":
			behind := false
			saved := p.lookbehind
			if char == "<" {
				c, ok, err := p.get()
				if err != nil {
					return nil, err
				}
				if !ok {
					return nil, p.errorf(0, "unexpected end of pattern")
				}
				if c != "=" && c != "!" {
					return nil, p.errorf(runeLen(c)+2, "unknown extension ?<%s", c)
				}
				char = c
				behind = true
				if p.lookbehind < 0 {
					p.lookbehind = p.groups()
				}
			}
			sub, err := p.parseSub(flags&^Verbose|boolFlag(verbose, Verbose), nested+1)
			if err != nil {
				return nil, err
			}
			if behind {
				p.lookbehind = saved
				w := p.width(sub)
				if w.lo > maxCode {
					return nil, &Error{Msg: "looks too much behind", Pattern: p.pattern, Pos: -1}
				}
				if w.lo != w.hi {
					return nil, &Error{Msg: "look-behind requires fixed-width pattern", Pattern: p.pattern, Pos: -1}
				}
			}
			if ok, err := p.match(")"); err != nil {
				return nil, err
			} else if !ok {
				return nil, p.errorf(p.tell()-start, "missing ), unterminated subpattern")
			}
			if char == "!" && isEmpty(sub) {
				return &node{kind: nFail}, nil
			}
			return &node{kind: nLook, behind: behind, negate: char == "!", subs: []*node{sub}}, nil
		case char == "(":
			condname, err := p.getuntil(")", "group name")
			if err != nil {
				return nil, err
			}
			var cond int
			if isDecimalASCII(condname) {
				v, err := strconv.ParseUint(condname, 10, 64)
				if err != nil || v >= maxGroups {
					return nil, p.errorf(runeLen(condname)+1, "invalid group reference %s", condname)
				}
				cond = int(v)
				if cond == 0 {
					return nil, p.errorf(runeLen(condname)+1, "bad group number")
				}
				if !slices.ContainsFunc(p.grouprefs, func(r groupref) bool { return r.group == cond }) {
					p.grouprefs = append(p.grouprefs, groupref{cond, p.tell() - runeLen(condname) - 1})
				}
			} else {
				if err := p.checkGroupName(condname, 1); err != nil {
					return nil, err
				}
				gid, ok := p.groupNames[condname]
				if !ok {
					return nil, p.errorf(runeLen(condname)+1, "unknown group name %s", pyRepr(condname))
				}
				cond = gid
			}
			if err := p.checkLookbehindGroup(cond); err != nil {
				return nil, err
			}
			yes, err := p.parseSeq(flags, verbose, nested+1, false)
			if err != nil {
				return nil, err
			}
			n := &node{kind: nCond, group: cond, subs: []*node{yes}}
			if ok, err := p.match("|"); err != nil {
				return nil, err
			} else if ok {
				no, err := p.parseSeq(flags, verbose, nested+1, false)
				if err != nil {
					return nil, err
				}
				if p.hasNext && p.next == "|" {
					return nil, p.errorf(0, "conditional backref with more than two branches")
				}
				n.no, n.hasNo = no, true
			}
			if ok, err := p.match(")"); err != nil {
				return nil, err
			} else if !ok {
				return nil, p.errorf(p.tell()-start, "missing ), unterminated subpattern")
			}
			return n, nil
		case char == ">":
			capture = false
			atomic = true
		case char == "-" || containsRune(inlineFlagChars, []rune(char)[0]):
			a, d, global, err := p.parseFlags(char)
			if err != nil {
				return nil, err
			}
			if global {
				if !first || len(seq.subs) > 0 {
					return nil, p.errorf(p.tell()-start, "global flags not at the start of the expression")
				}
				p.flags |= a
				return nil, nil
			}
			add, del = a, d
			capture = false
		default:
			return nil, p.errorf(runeLen(char)+1, "unknown extension ?%s", char)
		}
	}
	group := 0
	if capture {
		gid, err := p.openGroup(name, named)
		if err != nil {
			var e *Error
			if asError(err, &e) && e.Pos < 0 {
				return nil, p.errorf(runeLen(name)+1, "%s", e.Msg)
			}
			return nil, err
		}
		group = gid
	}
	if add&typeFlags != 0 {
		flags &^= typeFlags
	}
	subFlags := (flags | add) &^ del
	subVerbose := (verbose || add&Verbose != 0) && del&Verbose == 0
	subFlags = subFlags&^Verbose | boolFlag(subVerbose, Verbose)
	sub, err := p.parseSub(subFlags, nested+1)
	if err != nil {
		return nil, err
	}
	if ok, err := p.match(")"); err != nil {
		return nil, err
	} else if !ok {
		return nil, p.errorf(p.tell()-start, "missing ), unterminated subpattern")
	}
	if capture {
		w := p.width(sub)
		p.groupWidths[group] = &w
	}
	if atomic {
		return &node{kind: nAtomic, subs: []*node{sub}}, nil
	}
	return &node{kind: nGroup, group: group, subs: []*node{sub}}, nil
}

const inlineFlagChars = "iLmsxau"

func flagFor(c rune) Flags {
	switch c {
	case 'i':
		return IgnoreCase
	case 'L':
		return locale
	case 'm':
		return Multiline
	case 's':
		return DotAll
	case 'x':
		return Verbose
	case 'a':
		return ASCII
	case 'u':
		return unicodeFlag
	}
	return 0
}

const typeFlags = ASCII | locale | unicodeFlag

func (p *parser) parseFlags(char string) (add, del Flags, global bool, err error) {
	c := []rune(char)[0]
	if char != "-" {
		for {
			flag := flagFor(c)
			if p.text && c == 'L' {
				return 0, 0, false, p.errorf(0, "bad inline flags: cannot use 'L' flag with a str pattern")
			}
			if !p.text && c == 'u' {
				return 0, 0, false, p.errorf(0, "bad inline flags: cannot use 'u' flag with a bytes pattern")
			}
			add |= flag
			if flag&typeFlags != 0 && add&typeFlags != flag {
				return 0, 0, false, p.errorf(0, "bad inline flags: flags 'a', 'u' and 'L' are incompatible")
			}
			s, ok, gerr := p.get()
			if gerr != nil {
				return 0, 0, false, gerr
			}
			if !ok {
				return 0, 0, false, p.errorf(0, "missing -, : or )")
			}
			c = []rune(s)[0]
			char = s
			if s == ")" || s == "-" || s == ":" {
				break
			}
			if runeLen(s) != 1 || !containsRune(inlineFlagChars, c) {
				if unicode.IsLetter(c) {
					return 0, 0, false, p.errorf(runeLen(s), "unknown flag")
				}
				return 0, 0, false, p.errorf(runeLen(s), "missing -, : or )")
			}
		}
	}
	if char == ")" {
		return add, 0, true, nil
	}
	if char == "-" {
		s, ok, gerr := p.get()
		if gerr != nil {
			return 0, 0, false, gerr
		}
		if !ok {
			return 0, 0, false, p.errorf(0, "missing flag")
		}
		c = []rune(s)[0]
		if runeLen(s) != 1 || !containsRune(inlineFlagChars, c) {
			if unicode.IsLetter(c) {
				return 0, 0, false, p.errorf(runeLen(s), "unknown flag")
			}
			return 0, 0, false, p.errorf(runeLen(s), "missing flag")
		}
		for {
			flag := flagFor(c)
			if flag&typeFlags != 0 {
				return 0, 0, false, p.errorf(0, "bad inline flags: cannot turn off flags 'a', 'u' and 'L'")
			}
			del |= flag
			s, ok, gerr := p.get()
			if gerr != nil {
				return 0, 0, false, gerr
			}
			if !ok {
				return 0, 0, false, p.errorf(0, "missing :")
			}
			if s == ":" {
				break
			}
			c = []rune(s)[0]
			if runeLen(s) != 1 || !containsRune(inlineFlagChars, c) {
				if unicode.IsLetter(c) {
					return 0, 0, false, p.errorf(runeLen(s), "unknown flag")
				}
				return 0, 0, false, p.errorf(runeLen(s), "missing :")
			}
		}
	}
	if add&del != 0 {
		return 0, 0, false, p.errorf(1, "bad inline flags: flag turned on and off")
	}
	return add, del, false, nil
}

func (p *parser) width(n *node) width {
	switch n.kind {
	case nLiteral, nAny, nClass, nCategory:
		return width{1, 1}
	case nAt, nLook, nFail:
		return width{0, 0}
	case nGroup, nAtomic:
		return p.width(n.subs[0])
	case nBackref:
		if w := p.groupWidths[n.group]; w != nil {
			return *w
		}
		return width{0, 0}
	case nRepeat:
		w := p.width(n.subs[0])
		hi := satMul(w.hi, n.max)
		if n.max == maxRepeat && w.hi > 0 {
			hi = maxWidth
		}
		return width{satMul(w.lo, n.min), hi}
	case nCond:
		yes := p.width(n.subs[0])
		if !n.hasNo {
			return width{0, yes.hi}
		}
		no := p.width(n.no)
		return width{min(yes.lo, no.lo), max(yes.hi, no.hi)}
	case nAlt:
		w := width{maxWidth, 0}
		for _, s := range n.subs {
			sw := p.width(s)
			w.lo = min(w.lo, sw.lo)
			w.hi = max(w.hi, sw.hi)
		}
		return w
	case nConcat:
		var w width
		for _, s := range n.subs {
			sw := p.width(s)
			w.lo = satAdd(w.lo, sw.lo)
			w.hi = satAdd(w.hi, sw.hi)
		}
		return w
	}
	return width{0, 0}
}

func isEmpty(n *node) bool {
	return n.kind == nConcat && len(n.subs) == 0
}

type parsed struct {
	root   *node
	flags  Flags
	groups int
	names  map[string]int
}

func parse(pattern string, flags Flags) (*parsed, error) {
	text := flags&Bytes == 0
	if text && flags&locale != 0 {
		return nil, &Error{Msg: "cannot use LOCALE flag with a str pattern", Pattern: pattern, Pos: -1}
	}
	if flags&Bytes != 0 {
		for _, r := range pattern {
			if r > 0xff {
				return nil, &Error{Msg: "bytes pattern has a character above U+00FF", Pattern: pattern, Pos: -1}
			}
		}
	}
	p := &parser{
		src:         []rune(pattern),
		pattern:     pattern,
		text:        text,
		flags:       flags,
		groupNames:  map[string]int{},
		groupWidths: []*width{nil},
		lookbehind:  -1,
	}
	if err := p.advance(); err != nil {
		return nil, err
	}
	root, err := p.parseSub(flags, 0)
	if err != nil {
		return nil, err
	}
	if p.flags&locale != 0 {
		return nil, &Error{Msg: "the LOCALE flag is not supported", Pattern: pattern, Pos: -1}
	}
	if p.hasNext {
		return nil, p.errorf(0, "unbalanced parenthesis")
	}
	for _, r := range p.grouprefs {
		if r.group >= p.groups() {
			return nil, &Error{Msg: fmt.Sprintf("invalid group reference %d", r.group), Pattern: pattern, Pos: r.pos}
		}
	}
	return &parsed{root: root, flags: p.flags, groups: p.groups() - 1, names: p.groupNames}, nil
}

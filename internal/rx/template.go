package rx

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jeiang/atrium/internal/py"
)

type template struct {
	literals []string
	groups   []int
}

func (t *template) expand(m *Match) string {
	var b strings.Builder
	for i, lit := range t.literals {
		b.WriteString(lit)
		if i < len(t.groups) {
			b.WriteString(m.Group(t.groups[i]))
		}
	}
	return b.String()
}

// parseTemplate is Python's re._parser.parse_template.
func parseTemplate(repl string, re *Regexp) (*template, error) {
	s := &parser{src: []rune(repl), pattern: repl, text: re.flags&Bytes == 0}
	if err := s.advance(); err != nil {
		return nil, err
	}
	t := &template{}
	var lit strings.Builder
	addGroup := func(index, pos int) error {
		if index > re.groups {
			return s.errorf(pos, "invalid group reference %d", index)
		}
		t.literals = append(t.literals, lit.String())
		lit.Reset()
		t.groups = append(t.groups, index)
		return nil
	}
	for {
		this, ok, err := s.get()
		if err != nil {
			return nil, err
		}
		if !ok {
			break
		}
		r := []rune(this)
		if r[0] != '\\' {
			lit.WriteString(this)
			continue
		}
		c := r[1]
		switch {
		case c == 'g':
			if ok, err := s.match("<"); err != nil {
				return nil, err
			} else if !ok {
				return nil, s.errorf(0, "missing <")
			}
			name, err := s.getuntil(">", "group name")
			if err != nil {
				return nil, err
			}
			var index int
			if isDecimalASCII(name) {
				v, err := strconv.ParseUint(name, 10, 64)
				if err != nil || v >= maxGroups {
					return nil, s.errorf(runeLen(name)+1, "invalid group reference %s", name)
				}
				index = int(v)
			} else {
				if err := s.checkGroupName(name, 1); err != nil {
					return nil, err
				}
				gid, ok := re.names[name]
				if !ok {
					return nil, &Error{Msg: "unknown group name " + py.Repr(name), Pattern: repl, Pos: -1}
				}
				index = gid
			}
			if err := addGroup(index, runeLen(name)+1); err != nil {
				return nil, err
			}
		case c == '0':
			oct := "0"
			for range 2 {
				if !s.nextIn(octDigits) {
					break
				}
				d, _, err := s.get()
				if err != nil {
					return nil, err
				}
				oct += d
			}
			v, _ := strconv.ParseUint(oct, 8, 32)
			lit.WriteRune(rune(v & 0xff))
		case c >= '0' && c <= '9':
			isOctal := false
			if s.nextIn(digits) {
				d, _, err := s.get()
				if err != nil {
					return nil, err
				}
				this += d
				if c <= '7' && []rune(this)[2] <= '7' && s.nextIn(octDigits) {
					d, _, err := s.get()
					if err != nil {
						return nil, err
					}
					this += d
					isOctal = true
					v, _ := strconv.ParseUint(this[1:], 8, 32)
					if v > 0o377 {
						return nil, s.errorf(runeLen(this), "octal escape value %s outside of range 0-0o377", this)
					}
					lit.WriteRune(rune(v))
				}
			}
			if !isOctal {
				v, _ := strconv.Atoi(this[1:])
				if err := addGroup(v, runeLen(this)-1); err != nil {
					return nil, err
				}
			}
		default:
			if v, ok := simpleEscapes[c]; ok {
				lit.WriteRune(v)
				continue
			}
			if isASCIILetter(c) {
				return nil, s.errorf(runeLen(this), "bad escape %s", this)
			}
			lit.WriteString(this)
		}
	}
	t.literals = append(t.literals, lit.String())
	return t, nil
}

func runeLen(s string) int { return utf8.RuneCountInString(s) }

func containsRune(set string, r rune) bool { return strings.ContainsRune(set, r) }

func boolFlag(on bool, f Flags) Flags {
	if on {
		return f
	}
	return 0
}

func asError(err error, target **Error) bool { return errors.As(err, target) }

func isASCII(s string) bool {
	for _, r := range s {
		if r >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

func isDecimalASCII(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// isIdentifier approximates Python's str.isidentifier (XID_Start/XID_Continue) with Unicode letter and digit
// categories.
func isIdentifier(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		start := r == '_' || unicode.IsLetter(r) || unicode.Is(unicode.Nl, r)
		if i == 0 && !start {
			return false
		}
		if !start && !unicode.In(r, unicode.Mn, unicode.Mc, unicode.Nd, unicode.Pc) {
			return false
		}
	}
	return true
}

func pyASCIIRepr(s string) string {
	var b strings.Builder
	for _, r := range py.Repr(s) {
		switch {
		case r < 0x80:
			b.WriteRune(r)
		case r <= 0xff:
			fmt.Fprintf(&b, `\x%02x`, r)
		case r <= 0xffff:
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			fmt.Fprintf(&b, `\U%08x`, r)
		}
	}
	return b.String()
}

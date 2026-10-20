package rx

import (
	"container/list"
	"errors"
	"maps"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/dlclark/regexp2"

	"github.com/jeiang/atrium/internal/logx"
)

var log = logx.Logger("atrium.rx")

type Flags uint16

const (
	IgnoreCase Flags = 1 << iota
	Multiline
	DotAll
	Verbose
	ASCII
	Bytes
	locale
	unicodeFlag
)

const MatchTimeout = 100 * time.Millisecond

var ErrTimeout = errors.New("rx: match timed out")

var ErrGroups = errors.New("rx: FindAll needs a pattern with at most one group")

const cacheSize = 512

type Regexp struct {
	pattern  string
	flags    Flags
	groups   int
	names    map[string]int
	search   *regexp2.Regexp
	advance  *regexp2.Regexp
	anchored *regexp2.Regexp
	full     *regexp2.Regexp
}

type cacheKey struct {
	pattern string
	flags   Flags
}

type cacheEntry struct {
	key cacheKey
	re  *Regexp
}

var cache = struct {
	sync.Mutex
	order *list.List
	items map[cacheKey]*list.Element
}{order: list.New(), items: map[cacheKey]*list.Element{}}

func Compile(pattern string, flags Flags) (*Regexp, error) {
	key := cacheKey{pattern, flags}
	cache.Lock()
	if el, ok := cache.items[key]; ok {
		cache.order.MoveToFront(el)
		cache.Unlock()
		return el.Value.(*cacheEntry).re, nil
	}
	cache.Unlock()
	re, err := compile(pattern, flags)
	if err != nil {
		return nil, err
	}
	cache.Lock()
	defer cache.Unlock()
	if el, ok := cache.items[key]; ok {
		cache.order.MoveToFront(el)
		return el.Value.(*cacheEntry).re, nil
	}
	cache.items[key] = cache.order.PushFront(&cacheEntry{key, re})
	if cache.order.Len() > cacheSize {
		oldest := cache.order.Back()
		cache.order.Remove(oldest)
		delete(cache.items, oldest.Value.(*cacheEntry).key)
	}
	return re, nil
}

func MustCompile(pattern string, flags Flags) *Regexp {
	re, err := Compile(pattern, flags)
	if err != nil {
		panic("rx: " + err.Error() + ": " + pattern)
	}
	return re
}

func compile(pattern string, flags Flags) (*Regexp, error) {
	if flags&(locale|unicodeFlag) != 0 {
		return nil, &Error{Msg: "unsupported flags", Pattern: pattern, Pos: -1}
	}
	p, err := parse(pattern, flags)
	if err != nil {
		return nil, err
	}
	if p.flags&Bytes == 0 && p.flags&ASCII != 0 && p.flags&unicodeFlag != 0 {
		return nil, &Error{Msg: "ASCII and UNICODE flags are incompatible", Pattern: pattern, Pos: -1}
	}
	body := emitPattern(p.root)
	opts := regexp2.RegexOptions(regexp2.None)
	re := &Regexp{pattern: pattern, flags: flags, groups: p.groups, names: p.names}
	for _, v := range []struct {
		dst  **regexp2.Regexp
		expr string
	}{
		{&re.search, body},
		{&re.advance, `(?:` + body + `)(?!\G)`},
		{&re.anchored, `\G(?:` + body + `)`},
		{&re.full, `\G(?:` + body + `)\z`},
	} {
		c, err := regexp2.Compile(v.expr, opts)
		if err != nil {
			return nil, &Error{Msg: "regexp2 rejected the translated pattern: " + err.Error(), Pattern: pattern, Pos: -1}
		}
		c.MatchTimeout = MatchTimeout
		*v.dst = c
	}
	return re, nil
}

func (re *Regexp) String() string { return re.pattern }

func (re *Regexp) Flags() Flags { return re.flags }

func (re *Regexp) Groups() int { return re.groups }

func (re *Regexp) GroupIndex() map[string]int { return maps.Clone(re.names) }

type Match struct {
	re    *Regexp
	text  []rune
	spans []int
}

func (m *Match) Start() int { return m.spans[0] }

func (m *Match) End() int { return m.spans[1] }

func (m *Match) Text() string { return string(m.text[m.spans[0]:m.spans[1]]) }

func (m *Match) Span(i int) (int, int) { return m.spans[2*i], m.spans[2*i+1] }

func (m *Match) GroupOK(i int) (string, bool) {
	s, e := m.Span(i)
	if s < 0 {
		return "", false
	}
	return string(m.text[s:e]), true
}

func (m *Match) Group(i int) string {
	s, _ := m.GroupOK(i)
	return s
}

func (m *Match) Named(name string) (string, bool) {
	i, ok := m.re.names[name]
	if !ok {
		panic("rx: no such group " + name)
	}
	return m.GroupOK(i)
}

func (re *Regexp) find(prog *regexp2.Regexp, text []rune, pos int) (*Match, error) {
	m, err := prog.FindRunesMatchStartingAt(text, pos)
	if err != nil {
		log.Warn("rx: match timed out", "pattern", re.pattern, "timeout", MatchTimeout)
		return nil, ErrTimeout
	}
	if m == nil {
		return nil, nil
	}
	spans := make([]int, 2*(re.groups+1))
	for i := 0; i <= re.groups; i++ {
		g := m.GroupByNumber(i)
		if g == nil || len(g.Captures) == 0 {
			spans[2*i], spans[2*i+1] = -1, -1
			continue
		}
		spans[2*i], spans[2*i+1] = g.Index, g.Index+g.Length
	}
	return &Match{re: re, text: text, spans: spans}, nil
}

func bounds(text []rune, pos, endpos int) (int, int) {
	n := len(text)
	pos = min(max(pos, 0), n)
	endpos = min(max(endpos, 0), n)
	return pos, endpos
}

func (re *Regexp) one(prog *regexp2.Regexp, s string, pos, endpos int) (*Match, error) {
	text := []rune(s)
	pos, endpos = bounds(text, pos, endpos)
	if pos > endpos {
		return nil, nil
	}
	m, err := re.find(prog, text[:endpos], pos)
	if m != nil {
		m.text = text
	}
	return m, err
}

func (re *Regexp) Search(s string) (*Match, error) { return re.one(re.search, s, 0, math.MaxInt) }

func (re *Regexp) SearchRange(s string, pos, endpos int) (*Match, error) {
	return re.one(re.search, s, pos, endpos)
}

func (re *Regexp) Match(s string) (*Match, error) { return re.one(re.anchored, s, 0, math.MaxInt) }

func (re *Regexp) MatchRange(s string, pos, endpos int) (*Match, error) {
	return re.one(re.anchored, s, pos, endpos)
}

func (re *Regexp) FullMatch(s string) (*Match, error) { return re.one(re.full, s, 0, math.MaxInt) }

func (re *Regexp) FullMatchRange(s string, pos, endpos int) (*Match, error) {
	return re.one(re.full, s, pos, endpos)
}

func (re *Regexp) each(text []rune, pos, endpos, limit int, yield func(*Match)) error {
	pos, endpos = bounds(text, pos, endpos)
	if pos > endpos {
		return nil
	}
	t := text[:endpos]
	mustAdvance := false
	for n := 0; limit == 0 || n < limit; n++ {
		prog := re.search
		if mustAdvance {
			prog = re.advance
		}
		m, err := re.find(prog, t, pos)
		if err != nil {
			return err
		}
		if m == nil {
			return nil
		}
		m.text = text
		yield(m)
		mustAdvance = m.Start() == m.End()
		pos = m.End()
	}
	return nil
}

func (re *Regexp) FindIter(s string) ([]*Match, error) {
	return re.FindIterRange(s, 0, math.MaxInt)
}

func (re *Regexp) FindIterRange(s string, pos, endpos int) ([]*Match, error) {
	var out []*Match
	if err := re.each([]rune(s), pos, endpos, 0, func(m *Match) { out = append(out, m) }); err != nil {
		return nil, err
	}
	return out, nil
}

func (re *Regexp) FindAll(s string) ([]string, error) {
	if re.groups > 1 {
		return nil, ErrGroups
	}
	var out []string
	err := re.each([]rune(s), 0, math.MaxInt, 0, func(m *Match) { out = append(out, m.Group(re.groups)) })
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (re *Regexp) FindAllGroups(s string) ([][]string, error) {
	var out [][]string
	err := re.each([]rune(s), 0, math.MaxInt, 0, func(m *Match) {
		row := make([]string, re.groups)
		for i := range row {
			row[i] = m.Group(i + 1)
		}
		out = append(out, row)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (re *Regexp) Sub(repl, s string, count int) (string, error) {
	out, _, err := re.SubN(repl, s, count)
	return out, err
}

func (re *Regexp) SubN(repl, s string, count int) (string, int, error) {
	tpl, err := parseTemplate(repl, re)
	if err != nil {
		return "", 0, err
	}
	return re.subFunc(s, tpl.expand, count)
}

func (re *Regexp) SubFunc(s string, fn func(*Match) string, count int) (string, error) {
	out, _, err := re.subFunc(s, fn, count)
	return out, err
}

func (re *Regexp) subFunc(s string, fn func(*Match) string, count int) (string, int, error) {
	if count < 0 {
		return s, 0, nil
	}
	text := []rune(s)
	var b strings.Builder
	last, n := 0, 0
	err := re.each(text, 0, math.MaxInt, count, func(m *Match) {
		b.WriteString(string(text[last:m.Start()]))
		b.WriteString(fn(m))
		last = m.End()
		n++
	})
	if err != nil {
		return s, 0, err
	}
	if n == 0 {
		return s, 0, nil
	}
	b.WriteString(string(text[last:]))
	return b.String(), n, nil
}

func (re *Regexp) Split(s string, maxsplit int) ([]string, error) {
	text := []rune(s)
	if maxsplit < 0 {
		return []string{s}, nil
	}
	var out []string
	last := 0
	err := re.each(text, 0, math.MaxInt, maxsplit, func(m *Match) {
		out = append(out, string(text[last:m.Start()]))
		for i := 1; i <= re.groups; i++ {
			out = append(out, m.Group(i))
		}
		last = m.End()
	})
	if err != nil {
		return []string{s}, err
	}
	return append(out, string(text[last:])), nil
}

func Escape(s string) string {
	var b strings.Builder
	for _, r := range s {
		if strings.ContainsRune("()[]{}?*+-|^$\\.&~# \t\n\r\v\f", r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

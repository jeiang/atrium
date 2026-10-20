package mime

import (
	"errors"
	"strings"
)

type Addr struct {
	Name, Address string
}

type addrParser struct {
	field       []rune
	pos         int
	comments    []string
	depth       int
	recursedOut bool
}

const (
	addrSpecials = `()<>@,:;."[]`
	addrLWS      = " \t"
	addrFWS      = " \t\r\n"
	addrAtomEnds = addrSpecials + addrLWS + "\r\n"
	addrPhrEnds  = `()<>@,:;"[]` + addrLWS + "\r\n"
)

func (p *addrParser) at(set string) bool { return p.pos < len(p.field) && inSet(set, p.field[p.pos]) }

func (p *addrParser) gotonext() string {
	var ws []rune
	for p.pos < len(p.field) {
		c := p.field[p.pos]
		switch {
		case inSet(addrLWS+"\n\r", c):
			if c != '\n' && c != '\r' {
				ws = append(ws, c)
			}
			p.pos++
		case c == '(':
			p.comments = append(p.comments, p.getcomment())
		default:
			return string(ws)
		}
	}
	return string(ws)
}

func (p *addrParser) getaddrlist() []Addr {
	var out []Addr
	for p.pos < len(p.field) {
		ad := p.getaddress()
		if len(ad) > 0 {
			out = append(out, ad...)
		} else {
			out = append(out, Addr{})
		}
	}
	return out
}

func (p *addrParser) getaddress() []Addr {
	p.comments = nil
	p.gotonext()
	oldpos, oldcl := p.pos, p.comments
	plist := p.getphraselist()
	p.gotonext()
	var ret []Addr
	switch {
	case p.pos >= len(p.field):
		if len(plist) > 0 {
			ret = []Addr{{strings.Join(p.comments, " "), plist[0]}}
		}
	case p.at(".@"):
		p.pos, p.comments = oldpos, oldcl
		spec := p.getaddrspec()
		ret = []Addr{{strings.Join(p.comments, " "), spec}}
	case p.field[p.pos] == ':':
		p.pos++
		for p.pos < len(p.field) {
			p.gotonext()
			if p.pos < len(p.field) && p.field[p.pos] == ';' {
				p.pos++
				break
			}
			ret = append(ret, p.getaddress()...)
		}
	case p.field[p.pos] == '<':
		route := p.getrouteaddr()
		if len(p.comments) > 0 {
			ret = []Addr{{strings.Join(plist, " ") + " (" + strings.Join(p.comments, " ") + ")", route}}
		} else {
			ret = []Addr{{strings.Join(plist, " "), route}}
		}
	default:
		if len(plist) > 0 {
			ret = []Addr{{strings.Join(p.comments, " "), plist[0]}}
		} else if p.at(addrSpecials) {
			p.pos++
		}
	}
	p.gotonext()
	if p.pos < len(p.field) && p.field[p.pos] == ',' {
		p.pos++
	}
	return ret
}

func (p *addrParser) getrouteaddr() string {
	if p.field[p.pos] != '<' {
		return ""
	}
	expectRoute := false
	p.pos++
	p.gotonext()
	adlist := ""
	for p.pos < len(p.field) {
		switch {
		case expectRoute:
			p.getdomain()
			expectRoute = false
		case p.field[p.pos] == '>':
			p.pos++
			return adlist
		case p.field[p.pos] == '@':
			p.pos++
			expectRoute = true
		case p.field[p.pos] == ':':
			p.pos++
		default:
			adlist = p.getaddrspec()
			p.pos++
			return adlist
		}
		p.gotonext()
	}
	return adlist
}

func (p *addrParser) getaddrspec() string {
	var as []string
	p.gotonext()
	for p.pos < len(p.field) {
		preserve := true
		c := p.field[p.pos]
		switch {
		case c == '.':
			if len(as) > 0 && strings.TrimFunc(as[len(as)-1], isSpace) == "" {
				as = as[:len(as)-1]
			}
			as = append(as, ".")
			p.pos++
			preserve = false
		case c == '"':
			as = append(as, `"`+quote(p.getquote())+`"`)
		case inSet(addrAtomEnds, c):
			if len(as) > 0 && strings.TrimFunc(as[len(as)-1], isSpace) == "" {
				as = as[:len(as)-1]
			}
			goto done
		default:
			as = append(as, p.getatom(addrAtomEnds))
		}
		if ws := p.gotonext(); preserve && ws != "" {
			as = append(as, ws)
		}
	}
done:
	if p.pos >= len(p.field) || p.field[p.pos] != '@' {
		return strings.Join(as, "")
	}
	as = append(as, "@")
	p.pos++
	p.gotonext()
	domain := p.getdomain()
	if domain == "" {
		return ""
	}
	return strings.Join(as, "") + domain
}

func (p *addrParser) getdomain() string {
	var sd []string
	for p.pos < len(p.field) {
		c := p.field[p.pos]
		switch {
		case inSet(addrLWS, c):
			p.pos++
		case c == '(':
			p.comments = append(p.comments, p.getcomment())
		case c == '[':
			sd = append(sd, "["+p.getdelimited('[', "]\r", false)+"]")
		case c == '.':
			p.pos++
			sd = append(sd, ".")
		case c == '@':
			return ""
		case inSet(addrAtomEnds, c):
			return strings.Join(sd, "")
		default:
			sd = append(sd, p.getatom(addrAtomEnds))
		}
	}
	return strings.Join(sd, "")
}

func (p *addrParser) getdelimited(begin rune, ends string, comments bool) string {
	if p.field[p.pos] != begin {
		return ""
	}
	var sl []string
	quoted := false
	p.pos++
	for p.pos < len(p.field) {
		c := p.field[p.pos]
		switch {
		case quoted:
			sl = append(sl, string(c))
			quoted = false
		case inSet(ends, c):
			p.pos++
			return strings.Join(sl, "")
		case comments && c == '(':
			sl = append(sl, p.getcomment())
			continue
		case c == '\\':
			quoted = true
		default:
			sl = append(sl, string(c))
		}
		p.pos++
	}
	return strings.Join(sl, "")
}

func (p *addrParser) getquote() string { return p.getdelimited('"', "\"\r", false) }

func (p *addrParser) getcomment() string {
	if p.depth >= addrCommentLimit {
		p.recursedOut = true
		p.pos = len(p.field)
		return ""
	}
	p.depth++
	defer func() { p.depth-- }()
	return p.getdelimited('(', ")\r", true)
}

func (p *addrParser) getatom(ends string) string {
	start := p.pos
	for p.pos < len(p.field) && !inSet(ends, p.field[p.pos]) {
		p.pos++
	}
	return string(p.field[start:p.pos])
}

func (p *addrParser) getphraselist() []string {
	var plist []string
	for p.pos < len(p.field) {
		c := p.field[p.pos]
		switch {
		case inSet(addrFWS, c):
			p.pos++
		case c == '"':
			plist = append(plist, p.getquote())
		case c == '(':
			p.comments = append(p.comments, p.getcomment())
		case inSet(addrPhrEnds, c):
			return plist
		default:
			plist = append(plist, p.getatom(addrPhrEnds))
		}
	}
	return plist
}

// addrCommentLimit is the comment nesting email.utils.getaddresses parses before RecursionError when
// features.extract runs four frames below a module.
const addrCommentLimit = 494

var errAddrTooDeep = errors.New("address comment nesting exceeds Python's recursion limit")

func addressList(field string) ([]Addr, error) {
	if field == "" {
		return nil, nil
	}
	p := &addrParser{field: []rune(field)}
	out := p.getaddrlist()
	if p.recursedOut {
		return nil, errAddrTooDeep
	}
	return out, nil
}

func escapedChars(s []rune, yield func(pos int, ch string)) {
	escape := false
	pos := 0
	for i, c := range s {
		pos = i
		switch {
		case escape:
			yield(i, `\`+string(c))
			escape = false
		case c == '\\':
			escape = true
		default:
			yield(i, string(c))
		}
	}
	if escape {
		yield(pos, `\`)
	}
}

func stripQuotedRealnames(addr string) string {
	if !strings.Contains(addr, `"`) {
		return addr
	}
	rs := []rune(addr)
	start, open := 0, -1
	var b strings.Builder
	escapedChars(rs, func(pos int, ch string) {
		if ch != `"` {
			return
		}
		if open < 0 {
			open = pos
			return
		}
		if start != open {
			b.WriteString(string(rs[start:open]))
		}
		start, open = pos+1, -1
	})
	if start < len(rs) {
		b.WriteString(string(rs[start:]))
	}
	return b.String()
}

func checkParenthesis(addr string) bool {
	opens := 0
	ok := true
	escapedChars([]rune(stripQuotedRealnames(addr)), func(_ int, ch string) {
		switch ch {
		case "(":
			opens++
		case ")":
			opens--
			if opens < 0 {
				ok = false
			}
		}
	})
	return ok && opens == 0
}

func postParse(addrs []Addr) []Addr {
	for i, a := range addrs {
		if strings.Contains(a.Address, "[") {
			addrs[i] = Addr{}
		}
	}
	return addrs
}

func ParseAddr(addr string) (Addr, error) {
	if !checkParenthesis(addr) {
		addr = "('', '')"
	}
	addrs, err := addressList(addr)
	if err != nil {
		return Addr{}, err
	}
	addrs = postParse(addrs)
	if len(addrs) != 1 {
		return Addr{}, nil
	}
	return addrs[0], nil
}

func GetAddresses(values []string) ([]Addr, error) {
	checked := make([]string, len(values))
	for i, v := range values {
		if !checkParenthesis(v) {
			v = "('', '')"
		}
		checked[i] = v
	}
	result, err := addressList(strings.Join(checked, ", "))
	if err != nil {
		return nil, err
	}
	result = postParse(result)
	n := 0
	for _, v := range checked {
		n += 1 + strings.Count(stripQuotedRealnames(v), ",")
	}
	if len(result) != n {
		return []Addr{{}}, nil
	}
	return result, nil
}

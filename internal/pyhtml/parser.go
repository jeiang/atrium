package pyhtml

import (
	"slices"
	"strings"

	"github.com/jeiang/atrium/internal/py"
)

type Attr struct {
	Name  string
	Value *string
}

type Parser struct {
	StartTag    func(tag string, attrs []Attr)
	EndTag      func(tag string)
	StartEndTag func(tag string, attrs []Attr)
	Data        func(data string)
	Comment     func(data string)
	Decl        func(decl string)
	PI          func(data string)
	UnknownDecl func(data string)
	Scripting   bool

	raw       []rune
	cdataElem string
	escapable bool
	started   bool
}

func (p *Parser) init() {
	if !p.started {
		p.started = true
		p.escapable = true
	}
}

func (p *Parser) Feed(data string) {
	p.init()
	p.raw = append(p.raw, []rune(data)...)
	p.goahead(false)
}

func (p *Parser) Close() {
	p.init()
	p.goahead(true)
}

func (p *Parser) data(s string) {
	if p.Data != nil {
		p.Data(s)
	}
}

func (p *Parser) comment(s string) {
	if p.Comment != nil {
		p.Comment(s)
	}
}

func isWS(c rune) bool { return c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == ' ' }

func isASCIILetter(c rune) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }

func hasPrefixAt(raw []rune, i int, prefix string) bool {
	if i+len(prefix) > len(raw) {
		return false
	}
	for j := range len(prefix) {
		if raw[i+j] != rune(prefix[j]) {
			return false
		}
	}
	return true
}

func hasPrefixFold(raw []rune, i int, prefix string) bool {
	if i+len(prefix) > len(raw) {
		return false
	}
	for j := range len(prefix) {
		r := raw[i+j]
		if r >= 'A' && r <= 'Z' {
			r += 'a' - 'A'
		}
		if r != rune(prefix[j]) {
			return false
		}
	}
	return true
}

func indexRune(raw []rune, c rune, from int) int {
	for i := from; i < len(raw); i++ {
		if raw[i] == c {
			return i
		}
	}
	return -1
}

func indexRunes(raw []rune, sub string, from int) int {
	for i := from; i+len(sub) <= len(raw); i++ {
		if hasPrefixAt(raw, i, sub) {
			return i
		}
	}
	return -1
}

func (p *Parser) searchInteresting(i int) int {
	raw := p.raw
	if p.cdataElem == "plaintext" {
		return len(raw)
	}
	elem := []rune(p.cdataElem)
	for j := i; j+2+len(elem) < len(raw); j++ {
		if raw[j] != '<' || raw[j+1] != '/' {
			continue
		}
		if !matchesFoldASCII(raw[j+2:j+2+len(elem)], elem) {
			continue
		}
		if c := raw[j+2+len(elem)]; isWS(c) || c == '/' || c == '>' {
			return j
		}
	}
	return -1
}

func matchesFoldASCII(got, want []rune) bool {
	for i, c := range got {
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		if c != want[i] {
			return false
		}
	}
	return true
}

func (p *Parser) setCDATAMode(elem string, escapable bool) {
	p.cdataElem = strings.ToLower(elem)
	p.escapable = escapable
}

func (p *Parser) clearCDATAMode() {
	p.cdataElem = ""
	p.escapable = true
}

func (p *Parser) goahead(end bool) {
	raw := p.raw
	i, n := 0, len(raw)
	for i < n {
		var j int
		if p.cdataElem == "" {
			j = indexRune(raw, '<', i)
			if j < 0 {
				amp := -1
				for k := n - 1; k >= max(i, n-34); k-- {
					if raw[k] == '&' {
						amp = k
						break
					}
				}
				if amp >= 0 && !slices.ContainsFunc(raw[amp:], func(c rune) bool { return isWS(c) || c == ';' }) {
					break
				}
				j = n
			}
		} else {
			j = p.searchInteresting(i)
			if j < 0 {
				break
			}
		}
		if i < j {
			if p.escapable {
				p.data(Unescape(string(raw[i:j])))
			} else {
				p.data(string(raw[i:j]))
			}
		}
		i = j
		if i == n {
			break
		}
		var k int
		switch {
		case i+1 < n && isASCIILetter(raw[i+1]):
			k = p.parseStartTag(i)
		case hasPrefixAt(raw, i, "</"):
			k = p.parseEndTag(i)
		case hasPrefixAt(raw, i, "<!--"):
			k = p.parseComment(i)
		case hasPrefixAt(raw, i, "<?"):
			k = p.parsePI(i)
		case hasPrefixAt(raw, i, "<!"):
			k = p.parseHTMLDeclaration(i)
		case i+1 < n || end:
			p.data("<")
			k = i + 1
		default:
			k = -2
		}
		if k == -2 {
			break
		}
		if k < 0 {
			if !end {
				break
			}
			p.finishIncomplete(i)
			k = n
		}
		i = k
	}
	if end && i < n {
		if p.escapable {
			p.data(Unescape(string(raw[i:n])))
		} else {
			p.data(string(raw[i:n]))
		}
		i = n
	}
	p.raw = raw[i:]
}

func (p *Parser) finishIncomplete(i int) {
	raw := p.raw
	n := len(raw)
	switch {
	case i+1 < n && isASCIILetter(raw[i+1]):
	case hasPrefixAt(raw, i, "</"):
		switch {
		case i+2 == n:
			p.data("</")
		case i+2 < n && isASCIILetter(raw[i+2]):
		default:
			p.comment(string(raw[i+2:]))
		}
	case hasPrefixAt(raw, i, "<!--"):
		j := n
		for _, suffix := range []string{"--!", "--", "-"} {
			if endsWithAt(raw, suffix, i+4) {
				j -= len(suffix)
				break
			}
		}
		p.comment(string(raw[i+4 : j]))
	case hasPrefixAt(raw, i, "<![CDATA["):
		if p.UnknownDecl != nil {
			p.UnknownDecl(string(raw[i+3:]))
		}
	case hasPrefixFold(raw, i, "<!doctype"):
		if p.Decl != nil {
			p.Decl(string(raw[i+2:]))
		}
	case hasPrefixAt(raw, i, "<!"):
		p.comment(string(raw[i+2:]))
	case hasPrefixAt(raw, i, "<?"):
		if p.PI != nil {
			p.PI(string(raw[i+2:]))
		}
	}
}

func endsWithAt(raw []rune, suffix string, start int) bool {
	s := []rune(suffix)
	return len(raw)-start >= len(s) && hasPrefixAt(raw, len(raw)-len(s), suffix)
}

func (p *Parser) parseHTMLDeclaration(i int) int {
	raw := p.raw
	switch {
	case hasPrefixAt(raw, i, "<![CDATA["):
		j := indexRunes(raw, "]]>", i+9)
		if j < 0 {
			return -1
		}
		if p.UnknownDecl != nil {
			p.UnknownDecl(string(raw[i+3 : j]))
		}
		return j + 3
	case hasPrefixFold(raw, i, "<!doctype"):
		gt := indexRune(raw, '>', i+9)
		if gt < 0 {
			return -1
		}
		if p.Decl != nil {
			p.Decl(string(raw[i+2 : gt]))
		}
		return gt + 1
	}
	return p.parseBogusComment(i)
}

func (p *Parser) parseComment(i int) int {
	raw := p.raw
	start := i + 4
	var cstart, cend int
	switch {
	case start < len(raw) && raw[start] == '>':
		cstart, cend = start, start+1
	case start+1 < len(raw) && raw[start] == '-' && raw[start+1] == '>':
		cstart, cend = start, start+2
	default:
		found := false
		for j := start; j+2 < len(raw); j++ {
			if raw[j] != '-' || raw[j+1] != '-' {
				continue
			}
			if raw[j+2] == '>' {
				cstart, cend, found = j, j+3, true
				break
			}
			if raw[j+2] == '!' && j+3 < len(raw) && raw[j+3] == '>' {
				cstart, cend, found = j, j+4, true
				break
			}
		}
		if !found {
			return -1
		}
	}
	p.comment(string(raw[start:cstart]))
	return cend
}

func (p *Parser) parseBogusComment(i int) int {
	raw := p.raw
	pos := indexRune(raw, '>', i+2)
	if pos < 0 {
		return -1
	}
	p.comment(string(raw[i+2 : pos]))
	return pos + 1
}

func (p *Parser) parsePI(i int) int {
	raw := p.raw
	j := indexRune(raw, '>', i+2)
	if j < 0 {
		return -1
	}
	if p.PI != nil {
		p.PI(string(raw[i+2 : j]))
	}
	return j + 1
}

func tagFind(raw []rune, pos int) (string, int) {
	k := pos + 1
	for k < len(raw) && !isWS(raw[k]) && raw[k] != '/' && raw[k] != '>' {
		k++
	}
	name := string(raw[pos:k])
	end := skipSpaceOrSlash(raw, k)
	return name, end
}

func skipSpaceOrSlash(raw []rune, k int) int {
	for k < len(raw) {
		switch {
		case isWS(raw[k]):
			k++
		case raw[k] == '/' && (k+1 >= len(raw) || raw[k+1] != '>'):
			k++
		default:
			return k
		}
	}
	return k
}

func skipWS(raw []rune, k int) int {
	for k < len(raw) && isWS(raw[k]) {
		k++
	}
	return k
}

func isLookbehind(c rune) bool { return c == '\'' || c == '"' || isWS(c) || c == '/' }

type attrMatch struct {
	name     [2]int
	hasValue bool
	value    [2]int
	valueEnd int
}

func matchAttr(raw []rune, k int) (attrMatch, bool) {
	var m attrMatch
	if k >= len(raw) || k == 0 || !isLookbehind(raw[k-1]) {
		return m, false
	}
	if c := raw[k]; isWS(c) || c == '/' || c == '>' {
		return m, false
	}
	q := k + 1
	for q < len(raw) && !isWS(raw[q]) && raw[q] != '/' && raw[q] != '=' && raw[q] != '>' {
		q++
	}
	m.name = [2]int{k, q}
	m.valueEnd = q
	r := skipWS(raw, q)
	if r >= len(raw) || raw[r] != '=' {
		return m, true
	}
	afterEq := r + 1
	s := skipWS(raw, afterEq)
	vend := -1
	if s < len(raw) && (raw[s] == '\'' || raw[s] == '"') {
		if c := indexRune(raw, raw[s], s+1); c >= 0 {
			vend = c + 1
		}
	} else {
		vend = s
		for vend < len(raw) && raw[vend] != '>' && !isWS(raw[vend]) {
			vend++
		}
	}
	if vend < 0 && s > afterEq {
		s--
		vend = s
	}
	if vend < 0 {
		return m, true
	}
	m.hasValue = true
	m.value = [2]int{s, vend}
	m.valueEnd = vend
	return m, true
}

func locateTagEnd(raw []rune, pos int) int {
	k := pos + 1
	for k < len(raw) && !isWS(raw[k]) && raw[k] != '/' && raw[k] != '>' {
		k++
	}
	for k < len(raw) && (isWS(raw[k]) || raw[k] == '/') {
		k++
	}
	for {
		m, ok := matchAttr(raw, k)
		if !ok {
			break
		}
		k = m.valueEnd
		for k < len(raw) && (isWS(raw[k]) || raw[k] == '/') {
			k++
		}
	}
	if k < len(raw) && raw[k] == '>' {
		k++
	}
	return k
}

func (p *Parser) parseStartTag(i int) int {
	raw := p.raw
	end := locateTagEnd(raw, i+1)
	if raw[end-1] != '>' {
		return -1
	}
	name, k := tagFind(raw, i+1)
	tag := py.Lower(name)
	var attrs []Attr
	for k < end {
		m, ok := matchAttr(raw, k)
		if !ok {
			break
		}
		var value *string
		if m.hasValue {
			v := string(raw[m.value[0]:m.value[1]])
			if len(v) >= 2 && (v[0] == '\'' && v[len(v)-1] == '\'' || v[0] == '"' && v[len(v)-1] == '"') {
				v = v[1 : len(v)-1]
			}
			if v != "" {
				v = unescapeAttrValue(v)
			}
			value = &v
		}
		attrs = append(attrs, Attr{Name: py.Lower(string(raw[m.name[0]:m.name[1]])), Value: value})
		k = skipSpaceOrSlash(raw, m.valueEnd)
	}
	rest := py.Strip(string(raw[k:end]))
	if rest != ">" && rest != "/>" {
		p.data(string(raw[i:end]))
		return end
	}
	if strings.HasSuffix(rest, "/>") {
		if p.StartEndTag != nil {
			p.StartEndTag(tag, attrs)
		} else {
			if p.StartTag != nil {
				p.StartTag(tag, attrs)
			}
			if p.EndTag != nil {
				p.EndTag(tag)
			}
		}
		return end
	}
	if p.StartTag != nil {
		p.StartTag(tag, attrs)
	}
	switch {
	case slices.Contains(cdataContentElements, tag), p.Scripting && tag == "noscript", tag == "plaintext":
		p.setCDATAMode(tag, false)
	case tag == "textarea" || tag == "title":
		p.setCDATAMode(tag, true)
	}
	return end
}

var cdataContentElements = []string{"script", "style", "xmp", "iframe", "noembed", "noframes"}

func (p *Parser) parseEndTag(i int) int {
	raw := p.raw
	if indexRune(raw, '>', i+2) < 0 {
		return -1
	}
	if i+2 >= len(raw) || !isASCIILetter(raw[i+2]) {
		if i+2 < len(raw) && raw[i+2] == '>' {
			return i + 3
		}
		return p.parseBogusComment(i)
	}
	j := locateTagEnd(raw, i+2)
	if raw[j-1] != '>' {
		return -1
	}
	name, _ := tagFind(raw, i+2)
	if p.EndTag != nil {
		p.EndTag(py.Lower(name))
	}
	p.clearCDATAMode()
	return j
}

func unescapeAttrValue(s string) string {
	rs := []rune(s)
	var b strings.Builder
	for i := 0; i < len(rs); {
		if rs[i] != '&' {
			b.WriteRune(rs[i])
			i++
			continue
		}
		j := i + 1
		switch {
		case j < len(rs) && rs[j] == '#':
			j++
			if j < len(rs) && (rs[j] == 'x' || rs[j] == 'X') {
				k := j + 1
				for k < len(rs) && isHexDigit(rs[k]) {
					k++
				}
				if k > j+1 {
					j = k
				} else {
					j = -1
				}
			} else {
				k := j
				for k < len(rs) && isDigit(rs[k]) {
					k++
				}
				if k > j {
					j = k
				} else {
					j = -1
				}
			}
			if j < 0 {
				b.WriteRune('&')
				i++
				continue
			}
		case j < len(rs) && isASCIILetter(rs[j]):
			for j < len(rs) && (isASCIILetter(rs[j]) || isDigit(rs[j])) {
				j++
			}
		default:
			b.WriteRune('&')
			i++
			continue
		}
		end := j
		if end < len(rs) && (rs[end] == ';' || rs[end] == '=') {
			end++
		}
		full := string(rs[i:end])
		switch {
		case strings.HasPrefix(full, "&#"):
			b.WriteString(Unescape(full))
		case !strings.HasSuffix(full, "=") && hasEntity(full[1:]):
			b.WriteString(Unescape(full))
		default:
			b.WriteString(full)
		}
		i = end
	}
	return b.String()
}

func hasEntity(name string) bool {
	_, ok := html5[name]
	return ok
}

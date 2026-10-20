package mime

import (
	"errors"
	"slices"
)

type class int

const (
	cList class = iota
	cWSList
	cComment
	cBareQuoted
	cQuoted
	cDisplayName
	cLocalPart
	cAddrSpec
	cDomain
	cDomainLiteral
	cMimeParams
	cParam
	cValue
	cAttribute
	cTermValue
	cTermWS
	cTermEWWS
)

type token struct {
	class   class
	typ     string
	text    []rune
	kids    []*token
	invalid bool
	mailbox bool
	defects int

	sectioned, extended bool
	number              int
	charset             []rune

	disposition    []rune
	hasDisposition bool
}

var errHeaderParse = errors.New("header parse error")

var errInvalidEW = errors.New("invalid encoded word")

const hardCommentDepth = 1000

// commentLimit is the deepest comment nesting CPython 3.14.7 parses before RecursionError, measured with
// features.extract called four frames below a module: each comment level costs two Python frames and each MIME
// level one. Positions in the grammar shift these by a frame or two; the values are those of the probed paths.
func commentLimit(kind string, level int, all bool) int {
	switch kind {
	case "address":
		if all {
			return 488
		}
		return 487
	case "message-id", "references":
		return 491
	case "content-disposition":
		return max(0, 489-3*level)
	}
	return max(0, 484-level/2)
}

func maxComment(t *token) int {
	if t == nil {
		return 0
	}
	if t.class == cComment {
		return t.number
	}
	n := 0
	for _, k := range t.kids {
		n = max(n, maxComment(k))
	}
	return n
}

func list(c class, typ string, kids ...*token) *token { return &token{class: c, typ: typ, kids: kids} }

func vterm(text []rune, typ string) *token { return &token{class: cTermValue, typ: typ, text: text} }

func wsterm(text []rune, typ string) *token { return &token{class: cTermWS, typ: typ, text: text} }

func (t *token) isTerm() bool { return t.class >= cTermValue }

func (t *token) addDefect(invalid bool) {
	t.defects++
	if invalid {
		t.invalid = true
	}
}

func (t *token) append(k ...*token) { t.kids = append(t.kids, k...) }

func (t *token) prepend(k *token) { t.kids = slices.Insert(t.kids, 0, k) }

func (t *token) last() *token { return t.kids[len(t.kids)-1] }

func (t *token) str() []rune {
	switch t.class {
	case cTermValue, cTermWS:
		return t.text
	case cTermEWWS:
		return nil
	case cComment:
		out := []rune{'('}
		for _, k := range t.kids {
			s := k.str()
			if k.typ == "comment" {
				out = append(out, s...)
				continue
			}
			for _, r := range s {
				if r == '\\' || r == '(' || r == ')' {
					out = append(out, '\\')
				}
				out = append(out, r)
			}
		}
		return append(out, ')')
	case cBareQuoted:
		return quoteString(t.joinStr())
	case cMimeParams:
		return t.mimeParamsStr()
	}
	return t.joinStr()
}

func (t *token) joinStr() []rune {
	var out []rune
	for _, k := range t.kids {
		out = append(out, k.str()...)
	}
	return out
}

func (t *token) value() []rune {
	switch t.class {
	case cTermValue:
		return t.text
	case cTermWS, cWSList, cComment:
		return []rune{' '}
	case cTermEWWS:
		return nil
	case cBareQuoted:
		return t.joinStr()
	case cDisplayName:
		return t.displayNameValue()
	case cLocalPart:
		if t.kids[0].typ == "quoted-string" {
			return t.kids[0].quotedValue()
		}
		return t.kids[0].value()
	case cAddrSpec:
		if len(t.kids) < 3 {
			return t.kids[0].value()
		}
		out := slices.Clone(rstrip(t.kids[0].value()))
		out = append(out, t.kids[1].value()...)
		return append(out, lstrip(t.kids[2].value())...)
	}
	return t.joinValue()
}

func (t *token) joinValue() []rune {
	var out []rune
	for _, k := range t.kids {
		out = append(out, k.value()...)
	}
	return out
}

func (t *token) allInvalid() bool {
	if t.invalid {
		return true
	}
	for _, k := range t.kids {
		if k.allInvalid() {
			return true
		}
	}
	return false
}

func (t *token) hasComments() bool {
	if t.isTerm() {
		return false
	}
	if t.class == cWSList || t.class == cComment {
		for _, k := range t.kids {
			if k.typ == "comment" {
				return true
			}
		}
		return t.class == cComment
	}
	for _, k := range t.kids {
		if k.hasComments() {
			return true
		}
	}
	return false
}

func (t *token) quotedValue() []rune {
	var out []rune
	for _, k := range t.kids {
		if k.typ == "bare-quoted-string" {
			out = append(out, k.str()...)
		} else {
			out = append(out, k.value()...)
		}
	}
	return out
}

func (t *token) bareValue() ([]rune, bool) {
	for _, k := range t.kids {
		if k.typ == "bare-quoted-string" {
			return k.value(), true
		}
	}
	return nil, false
}

func quoteString(s []rune) []rune {
	out := []rune{'"'}
	for _, r := range s {
		if r == '\\' || r == '"' {
			out = append(out, '\\')
		}
		out = append(out, r)
	}
	return append(out, '"')
}

func (t *token) displayName() ([]rune, bool) {
	if t.mailbox {
		if t.kids[0].typ == "name-addr" {
			return t.kids[0].displayName()
		}
		return nil, false
	}
	switch t.typ {
	case "display-name":
		return t.displayNameText(), true
	case "name-addr":
		if len(t.kids) == 1 {
			return nil, false
		}
		return t.kids[0].displayName()
	case "group":
		return t.kids[0].displayName()
	case "address":
		if t.kids[0].typ == "group" {
			return t.kids[0].displayName()
		}
	}
	return nil, false
}

func firstKid(t *token) *token {
	if len(t.kids) == 0 {
		panic(fatal{})
	}
	return t.kids[0]
}

func lastKid(t *token) *token {
	if len(t.kids) == 0 {
		panic(fatal{})
	}
	return t.last()
}

func (t *token) displayNameText() []rune {
	res := slices.Clone(t.kids)
	if len(res) == 0 {
		return nil
	}
	if res[0].typ == "cfws" {
		res = res[1:]
	} else if !res[0].isTerm() && firstKid(res[0]).typ == "cfws" {
		res[0] = list(cList, "", res[0].kids[1:]...)
	}
	if len(res) == 0 {
		panic(fatal{})
	}
	if l := res[len(res)-1]; l.typ == "cfws" {
		res = res[:len(res)-1]
	} else if !l.isTerm() && lastKid(l).typ == "cfws" {
		res[len(res)-1] = list(cList, "", l.kids[:len(l.kids)-1]...)
	}
	return list(cList, "", res...).joinValue()
}

func (t *token) displayNameValue() []rune {
	quote := t.defects > 0
	if !quote {
		for _, k := range t.kids {
			if k.typ == "quoted-string" {
				quote = true
			}
		}
	}
	if len(t.kids) == 0 || !quote {
		return t.joinValue()
	}
	pre := t.kids[0].typ == "cfws" || !t.kids[0].isTerm() && firstKid(t.kids[0]).typ == "cfws"
	post := t.last().typ == "cfws" || !t.last().isTerm() && lastKid(t.last()).typ == "cfws"
	var out []rune
	if pre {
		out = append(out, ' ')
	}
	out = append(out, quoteString(t.displayNameText())...)
	if post {
		out = append(out, ' ')
	}
	return out
}

func (t *token) localPart() ([]rune, bool) {
	if t.mailbox {
		return t.kids[0].localPart()
	}
	switch t.typ {
	case "local-part":
		return t.localPartText(), true
	case "addr-spec":
		return t.kids[0].localPart()
	case "angle-addr":
		for _, k := range t.kids {
			if k.typ == "addr-spec" {
				return k.localPart()
			}
		}
		return nil, false
	case "name-addr":
		return t.last().localPart()
	}
	return nil, false
}

var dotToken = &token{class: cTermValue, typ: "dot", text: []rune{'.'}}

func (t *token) localPartText() []rune {
	res := []*token{dotToken}
	last := dotToken
	lastIsTL := false
	toks := append(slices.Clone(t.kids[0].kids), dotToken)
	if t.kids[0].isTerm() {
		toks = []*token{dotToken}
	}
	for _, tok := range toks {
		if tok.typ == "cfws" {
			continue
		}
		if lastIsTL && tok.typ == "dot" && !last.isTerm() && len(last.kids) > 0 && last.last().typ == "cfws" {
			res[len(res)-1] = list(cList, "", last.kids[:len(last.kids)-1]...)
		}
		isTL := !tok.isTerm()
		if isTL && last.typ == "dot" && len(tok.kids) > 0 && tok.kids[0].typ == "cfws" {
			res = append(res, list(cList, "", tok.kids[1:]...))
		} else {
			res = append(res, tok)
		}
		last = res[len(res)-1]
		lastIsTL = isTL
	}
	return list(cList, "", res[1:len(res)-1]...).joinValue()
}

func (t *token) domain() ([]rune, bool) {
	if t.mailbox {
		return t.kids[0].domain()
	}
	switch t.typ {
	case "domain", "domain-literal":
		return joinRunes(splitSpace(t.joinValue()), ""), true
	case "addr-spec":
		if len(t.kids) < 3 {
			return nil, false
		}
		return t.last().domain()
	case "angle-addr":
		for _, k := range t.kids {
			if k.typ == "addr-spec" {
				return k.domain()
			}
		}
		return nil, false
	case "name-addr":
		return t.last().domain()
	}
	return nil, false
}

func joinRunes(parts [][]rune, sep string) []rune {
	var out []rune
	for i, p := range parts {
		if i > 0 {
			out = append(out, []rune(sep)...)
		}
		out = append(out, p...)
	}
	return out
}

func (t *token) allMailboxes() []*token {
	switch t.typ {
	case "address-list":
		var out []*token
		for _, k := range t.kids {
			if k.typ == "address" {
				out = append(out, k.allMailboxes()...)
			}
		}
		return out
	case "address":
		switch t.kids[0].typ {
		case "mailbox", "invalid-mailbox":
			return []*token{t.kids[0]}
		}
		return t.kids[0].allMailboxes()
	case "mailbox-list":
		var out []*token
		for _, k := range t.kids {
			if k.typ == "mailbox" || k.typ == "invalid-mailbox" {
				out = append(out, k)
			}
		}
		return out
	case "group-list":
		if len(t.kids) == 0 || t.kids[0].typ != "mailbox-list" {
			return nil
		}
		return t.kids[0].allMailboxes()
	case "group":
		if len(t.kids) < 3 || t.kids[2].typ != "group-list" {
			return nil
		}
		return t.kids[2].allMailboxes()
	}
	return nil
}

const (
	wsp           = " \t"
	cfwsLeader    = " \t("
	specials      = `()<>@,:;."\[]`
	atomEnds      = specials + wsp
	dotAtomEnds   = `()<>@,:;"\[] ` + "\t"
	phraseEnds    = `)<>@,:;\[]`
	tokenEnds     = `()<>@,:;"\[]/?= ` + "\t"
	attributeEnds = tokenEnds + "*'%"
	extAttrEnds   = tokenEnds + "*'"
	hexDigits     = "0123456789abcdefABCDEF"
)

func first(v []rune, set string) bool { return len(v) > 0 && inSet(set, v[0]) }

func wspSplit(v []rune) ([]rune, []rune) {
	for i, r := range v {
		if r == ' ' || r == '\t' {
			return v[:i], v[i:]
		}
	}
	return v, nil
}

func runUntil(v []rune, ends string) int {
	i := 0
	for i < len(v) && !inSet(ends, v[i]) {
		i++
	}
	return i
}

func getFWS(v []rune) (*token, []rune) {
	rest := lstrip(v)
	return wsterm(v[:len(v)-len(rest)], "fws"), rest
}

func splitAt(v []rune, sep string) ([]rune, []rune, bool) {
	i := indexRunes(v, []rune(sep))
	if i < 0 {
		return v, nil, false
	}
	return v[:i], v[i+len([]rune(sep)):], true
}

func getEncodedWord(v []rune, termType string) (*token, []rune, error) {
	ew := list(cList, "encoded-word")
	if !hasPrefixStr(v, "=?") {
		return nil, v, errHeaderParse
	}
	tok, rem, found := splitAt(v[2:], "?=")
	if !found {
		return nil, v, errHeaderParse
	}
	if len(rem) > 1 && inSet(hexDigits, rem[0]) && inSet(hexDigits, rem[1]) && countRune(tok, '?') < 2 {
		rest, rem2, _ := splitAt(rem, "?=")
		tok = append(append(slices.Clone(tok), '?', '='), rest...)
		rem = rem2
	}
	if len(splitSpace(tok)) > 1 {
		ew.addDefect(true)
	}
	text, invalid, err := decodeEW(tok)
	if err != nil {
		return nil, v, errInvalidEW
	}
	if invalid {
		ew.addDefect(false)
	}
	for len(text) > 0 {
		if inSet(wsp, text[0]) {
			var ws *token
			ws, text = getFWS(text)
			ew.append(ws)
			continue
		}
		var chars []rune
		chars, text = wspSplit(text)
		ew.append(vterm(chars, termType))
	}
	if len(rem) > 0 && !inSet(wsp, rem[0]) {
		ew.addDefect(true)
	}
	return ew, rem, nil
}

func decodeEW(tok []rune) ([]rune, bool, error) {
	if countRune(tok, '?') != 2 {
		return nil, false, errInvalidEW
	}
	q1 := indexRune(tok, '?')
	q2 := q1 + 1 + indexRune(tok[q1+1:], '?')
	charset, cte, encoded := tok[:q1], string(lowerRunes(tok[q1+1:q2])), tok[q2+1:]
	if i := indexRune(charset, '*'); i >= 0 {
		charset = charset[:i]
	}
	for _, r := range encoded {
		if r >= 0x80 && (r < surrogateBase+0x80 || r > surrogateBase+0xFF) {
			return nil, false, errInvalidEW
		}
	}
	raw := unescapeBytes(encoded)
	switch cte {
	case "q":
		raw = decodeQ(raw)
	case "b":
		raw = decodeB(raw)
	default:
		return nil, false, errInvalidEW
	}
	if hasSurrogates(charset) {
		out, _ := decodeASCII(raw, errSurrogateEscape)
		return out, true, nil
	}
	f, err := lookupCodec(string(charset))
	if errors.Is(err, errNul) {
		return nil, false, errInvalidEW
	}
	if err != nil {
		out, _ := decodeASCII(raw, errSurrogateEscape)
		return out, true, nil
	}
	if out, err := f(raw, errStrict); err == nil {
		return out, false, nil
	}
	out, err := f(raw, errSurrogateEscape)
	if err != nil {
		return nil, false, errInvalidEW
	}
	return out, true, nil
}

func rfc2047Search(tok []rune) bool {
	for i := 0; i+1 < len(tok); i++ {
		if tok[i] != '=' || tok[i+1] != '?' {
			continue
		}
		j := i + 2
		for j < len(tok) && tok[j] != '?' {
			j++
		}
		if j+2 >= len(tok) || !inSet("qQbB", tok[j+1]) || tok[j+2] != '?' {
			continue
		}
		for k := j + 3; k+1 < len(tok) && tok[k] != '\n'; k++ {
			if tok[k] == '?' && tok[k+1] == '=' {
				return true
			}
		}
	}
	return false
}

func getUnstructured(v []rune) *token {
	u := list(cList, "unstructured")
	for len(v) > 0 {
		if inSet(wsp, v[0]) {
			var t *token
			t, v = getFWS(v)
			u.append(t)
			continue
		}
		validEW := true
		if hasPrefixStr(v, "=?") {
			t, rest, err := getEncodedWord(v, "utext")
			switch {
			case errors.Is(err, errInvalidEW):
				validEW = false
			case err == nil:
				v = rest
				haveWS := true
				if len(u.kids) > 0 && u.last().typ != "fws" {
					haveWS = false
				}
				if haveWS && len(u.kids) > 1 && u.kids[len(u.kids)-2].typ == "encoded-word" {
					prev := u.last()
					u.kids[len(u.kids)-1] = &token{class: cTermEWWS, typ: "fws", text: prev.str()}
				}
				u.append(t)
				continue
			}
		}
		tok, rest := wspSplit(v)
		if validEW && rfc2047Search(tok) {
			i := indexRunes(v, []rune("=?"))
			tok, rest = v[:i], v[i+2:]
			rest = append([]rune{'=', '?'}, rest...)
			if i == 0 {
				tok, rest = nil, v
			}
		}
		u.append(vterm(tok, "utext"))
		v = rest
	}
	return u
}

func getPtextToEndchars(v []rune, ends string) ([]rune, []rune, bool) {
	if len(v) == 0 {
		return nil, nil, false
	}
	fragment, remainder := wspSplit(v)
	if len(fragment) == 0 {
		panic(fatal{})
	}
	var vchars []rune
	escape, hadQP := false, false
	pos := 0
	for pos = 0; pos < len(fragment); pos++ {
		c := fragment[pos]
		if c == '\\' {
			if escape {
				escape = false
				hadQP = true
			} else {
				escape = true
				continue
			}
		}
		if escape {
			escape = false
		} else if inSet(ends, c) {
			break
		}
		vchars = append(vchars, c)
	}
	rest := append(slices.Clone(fragment[pos:]), remainder...)
	return vchars, rest, hadQP
}

func getQPCtext(v []rune) (*token, []rune) {
	p, rest, _ := getPtextToEndchars(v, "()")
	return wsterm(p, "ptext"), rest
}

func getQContent(v []rune) (*token, []rune) {
	p, rest, _ := getPtextToEndchars(v, `"`)
	return vterm(p, "ptext"), rest
}

func getAtext(v []rune) (*token, []rune, error) {
	n := runUntil(v, atomEnds)
	if n == 0 {
		return nil, v, errHeaderParse
	}
	return vterm(v[:n], "atext"), v[n:], nil
}

func getBareQuotedString(v []rune) (*token, []rune, error) {
	if len(v) == 0 || v[0] != '"' {
		return nil, v, errHeaderParse
	}
	bqs := list(cBareQuoted, "bare-quoted-string")
	v = v[1:]
	if len(v) > 0 && v[0] == '"' {
		return bqs, v[1:], nil
	}
	for len(v) > 0 && v[0] != '"' {
		var t *token
		switch {
		case inSet(wsp, v[0]):
			t, v = getFWS(v)
		case hasPrefixStr(v, "=?"):
			validEW := false
			ew, rest, err := getEncodedWord(v, "vtext")
			if err == nil {
				t, v = ew, rest
				bqs.addDefect(true)
				validEW = true
			} else {
				t, v = getQContent(v)
			}
			if validEW && len(bqs.kids) > 1 && bqs.last().typ == "fws" && bqs.kids[len(bqs.kids)-2].typ == "encoded-word" {
				prev := bqs.last()
				bqs.kids[len(bqs.kids)-1] = &token{class: cTermEWWS, typ: "fws", text: prev.str()}
			}
		default:
			t, v = getQContent(v)
		}
		bqs.append(t)
	}
	if len(v) == 0 {
		bqs.addDefect(true)
		return bqs, v, nil
	}
	return bqs, v[1:], nil
}

func getComment(v []rune, depth int) (*token, []rune, error) {
	if len(v) > 0 && v[0] != '(' {
		return nil, v, errHeaderParse
	}
	if depth > hardCommentDepth {
		return nil, v, errFatal
	}
	c := list(cComment, "comment")
	c.number = 1
	v = v[1:]
	for len(v) > 0 && v[0] != ')' {
		var t *token
		var err error
		switch {
		case inSet(wsp, v[0]):
			t, v = getFWS(v)
		case v[0] == '(':
			t, v, err = getComment(v, depth+1)
			if err != nil {
				return nil, v, err
			}
			c.number = max(c.number, t.number+1)
		default:
			t, v = getQPCtext(v)
		}
		c.append(t)
	}
	if len(v) == 0 {
		c.addDefect(true)
		return c, v, nil
	}
	return c, v[1:], nil
}

var errFatal = errors.New("python raises an exception other than HeaderParseError")

func getCFWS(v []rune) (*token, []rune, error) {
	cfws := list(cWSList, "cfws")
	for len(v) > 0 && inSet(cfwsLeader, v[0]) {
		var t *token
		if inSet(wsp, v[0]) {
			t, v = getFWS(v)
		} else {
			var err error
			t, v, err = getComment(v, 0)
			if err != nil {
				return nil, v, err
			}
		}
		cfws.append(t)
	}
	return cfws, v, nil
}

func getQuotedString(v []rune) (*token, []rune, error) {
	qs := list(cQuoted, "quoted-string")
	if first(v, cfwsLeader) {
		t, rest, err := getCFWS(v)
		if err != nil {
			return nil, v, err
		}
		qs.append(t)
		v = rest
	}
	t, rest, err := getBareQuotedString(v)
	if err != nil {
		return nil, v, err
	}
	qs.append(t)
	v = rest
	if first(v, cfwsLeader) {
		t, rest, err := getCFWS(v)
		if err != nil {
			return nil, v, err
		}
		qs.append(t)
		v = rest
	}
	return qs, v, nil
}

func getAtom(v []rune) (*token, []rune, error) {
	atom := list(cList, "atom")
	if first(v, cfwsLeader) {
		t, rest, err := getCFWS(v)
		if err != nil {
			return nil, v, err
		}
		atom.append(t)
		v = rest
	}
	if first(v, atomEnds) {
		return nil, v, errHeaderParse
	}
	var t *token
	var err error
	if hasPrefixStr(v, "=?") {
		var rest []rune
		t, rest, err = getEncodedWord(v, "vtext")
		if err != nil {
			t, rest, err = getAtext(v)
		}
		v = rest
	} else {
		t, v, err = getAtext(v)
	}
	if err != nil {
		return nil, v, err
	}
	atom.append(t)
	if first(v, cfwsLeader) {
		t, rest, err := getCFWS(v)
		if err != nil {
			return nil, v, err
		}
		atom.append(t)
		v = rest
	}
	return atom, v, nil
}

func getDotAtomText(v []rune) (*token, []rune, error) {
	dat := list(cList, "dot-atom-text")
	if len(v) == 0 || inSet(atomEnds, v[0]) {
		return nil, v, errHeaderParse
	}
	for len(v) > 0 && !inSet(atomEnds, v[0]) {
		t, rest, err := getAtext(v)
		if err != nil {
			return nil, v, err
		}
		dat.append(t)
		v = rest
		if len(v) > 0 && v[0] == '.' {
			dat.append(dotToken)
			v = v[1:]
		}
	}
	if dat.last() == dotToken {
		return nil, v, errHeaderParse
	}
	return dat, v, nil
}

func getDotAtom(v []rune) (*token, []rune, error) {
	da := list(cList, "dot-atom")
	if inSet(cfwsLeader, v[0]) {
		t, rest, err := getCFWS(v)
		if err != nil {
			return nil, v, err
		}
		da.append(t)
		v = rest
	}
	var t *token
	var err error
	var rest []rune
	if hasPrefixStr(v, "=?") {
		t, rest, err = getEncodedWord(v, "vtext")
		if err != nil {
			t, rest, err = getDotAtomText(v)
		}
	} else {
		t, rest, err = getDotAtomText(v)
	}
	if err != nil {
		return nil, v, err
	}
	v = rest
	da.append(t)
	if first(v, cfwsLeader) {
		t, rest, err := getCFWS(v)
		if err != nil {
			return nil, v, err
		}
		da.append(t)
		v = rest
	}
	return da, v, nil
}

func getWord(v []rune) (*token, []rune, error) {
	var leader *token
	if inSet(cfwsLeader, v[0]) {
		var err error
		leader, v, err = getCFWS(v)
		if err != nil {
			return nil, v, err
		}
	}
	if len(v) == 0 {
		return nil, v, errHeaderParse
	}
	var t *token
	var err error
	switch {
	case v[0] == '"':
		t, v, err = getQuotedString(v)
	case inSet(specials, v[0]):
		return nil, v, errHeaderParse
	default:
		t, v, err = getAtom(v)
	}
	if err != nil {
		return nil, v, err
	}
	if leader != nil {
		t.prepend(leader)
	}
	return t, v, nil
}

func getPhrase(v []rune) (*token, []rune, error) {
	phrase := list(cList, "phrase")
	t, rest, err := getWord(v)
	switch {
	case errors.Is(err, errFatal):
		return nil, v, err
	case err != nil:
		phrase.addDefect(true)
	default:
		phrase.append(t)
		v = rest
	}
	for len(v) > 0 && !inSet(phraseEnds, v[0]) {
		if v[0] == '.' {
			phrase.append(dotToken)
			phrase.addDefect(false)
			v = v[1:]
			continue
		}
		t, rest, err := getWord(v)
		if err == nil {
			v = rest
			if t.kids[0].typ == "encoded-word" && len(phrase.kids) > 0 {
				p := phrase.last()
				if p.typ == "atom" && len(p.kids) > 1 && p.kids[len(p.kids)-2].typ == "encoded-word" &&
					p.last().typ == "cfws" && !p.last().hasComments() {
					p.kids[len(p.kids)-1] = &token{class: cTermEWWS, typ: "fws", text: p.str()}
				}
			}
		} else {
			if errors.Is(err, errFatal) || !inSet(cfwsLeader, v[0]) {
				return nil, v, err
			}
			t, v, err = getCFWS(v)
			if err != nil {
				return nil, v, err
			}
			phrase.addDefect(false)
		}
		phrase.append(t)
	}
	return phrase, v, nil
}

func getLocalPart(v []rune) (*token, []rune, error) {
	lp := list(cLocalPart, "local-part")
	var leader *token
	if first(v, cfwsLeader) {
		var err error
		leader, v, err = getCFWS(v)
		if err != nil {
			return nil, v, err
		}
	}
	if len(v) == 0 {
		return nil, v, errHeaderParse
	}
	t, rest, err := getDotAtom(v)
	if err != nil {
		if errors.Is(err, errFatal) {
			return nil, v, err
		}
		t, rest, err = getWord(v)
		if err != nil {
			if errors.Is(err, errFatal) || v[0] != '\\' && inSet(phraseEnds, v[0]) {
				return nil, v, err
			}
			t, rest = list(cList, ""), v
		}
	}
	v = rest
	if leader != nil {
		t.prepend(leader)
	}
	lp.append(t)
	if len(v) > 0 && (v[0] == '\\' || !inSet(phraseEnds, v[0])) {
		obs, rest, err := getObsLocalPart(append(slices.Clone(lp.str()), v...))
		if err != nil {
			return nil, v, err
		}
		v = rest
		lp.addDefect(obs.typ == "invalid-obs-local-part")
		lp.kids[0] = obs
	}
	return lp, v, nil
}

func getObsLocalPart(v []rune) (*token, []rune, error) {
	obs := list(cList, "obs-local-part")
	lastDot := false
	for len(v) > 0 && (v[0] == '\\' || !inSet(phraseEnds, v[0])) {
		if v[0] == '.' {
			if lastDot {
				obs.addDefect(true)
			}
			obs.append(dotToken)
			lastDot = true
			v = v[1:]
			continue
		}
		if v[0] == '\\' {
			obs.append(vterm(v[:1], "misplaced-special"))
			v = v[1:]
			obs.addDefect(true)
			lastDot = false
			continue
		}
		if len(obs.kids) > 0 && obs.last().typ != "dot" {
			obs.addDefect(true)
		}
		t, rest, err := getWord(v)
		if err == nil {
			lastDot = false
		} else {
			if errors.Is(err, errFatal) || !inSet(cfwsLeader, v[0]) {
				return nil, v, err
			}
			t, rest, err = getCFWS(v)
			if err != nil {
				return nil, v, err
			}
		}
		v = rest
		obs.append(t)
	}
	if len(obs.kids) == 0 {
		return nil, v, errHeaderParse
	}
	k := obs.kids
	if k[0].typ == "dot" || k[0].typ == "cfws" && len(k) > 1 && k[1].typ == "dot" {
		obs.addDefect(true)
	}
	if k[len(k)-1].typ == "dot" || k[len(k)-1].typ == "cfws" && len(k) > 1 && k[len(k)-2].typ == "dot" {
		obs.addDefect(true)
	}
	if obs.defects > 0 {
		obs.typ = "invalid-obs-local-part"
	}
	return obs, v, nil
}

func getDtext(v []rune) (*token, []rune) {
	p, rest, hadQP := getPtextToEndchars(v, "[]")
	t := vterm(p, "ptext")
	if hadQP {
		t.addDefect(false)
	}
	return t, rest
}

func checkEarlyDLEnd(v []rune, dl *token) bool {
	if len(v) > 0 {
		return false
	}
	dl.addDefect(true)
	dl.append(vterm([]rune("]"), "domain-literal-end"))
	return true
}

func getDomainLiteral(v []rune) (*token, []rune, error) {
	dl := list(cDomainLiteral, "domain-literal")
	if inSet(cfwsLeader, v[0]) {
		t, rest, err := getCFWS(v)
		if err != nil {
			return nil, v, err
		}
		dl.append(t)
		v = rest
	}
	if len(v) == 0 || v[0] != '[' {
		return nil, v, errHeaderParse
	}
	v = v[1:]
	dl.append(vterm([]rune("["), "domain-literal-start"))
	if checkEarlyDLEnd(v, dl) {
		return dl, v, nil
	}
	if inSet(wsp, v[0]) {
		var t *token
		t, v = getFWS(v)
		dl.append(t)
	}
	var t *token
	t, v = getDtext(v)
	dl.append(t)
	if checkEarlyDLEnd(v, dl) {
		return dl, v, nil
	}
	if inSet(wsp, v[0]) {
		t, v = getFWS(v)
		dl.append(t)
	}
	if checkEarlyDLEnd(v, dl) {
		return dl, v, nil
	}
	if v[0] != ']' {
		return nil, v, errHeaderParse
	}
	dl.append(vterm([]rune("]"), "domain-literal-end"))
	v = v[1:]
	if first(v, cfwsLeader) {
		t, rest, err := getCFWS(v)
		if err != nil {
			return nil, v, err
		}
		dl.append(t)
		v = rest
	}
	return dl, v, nil
}

func getDomain(v []rune) (*token, []rune, error) {
	d := list(cDomain, "domain")
	var leader *token
	if first(v, cfwsLeader) {
		var err error
		leader, v, err = getCFWS(v)
		if err != nil {
			return nil, v, err
		}
	}
	if len(v) == 0 {
		return nil, v, errHeaderParse
	}
	if v[0] == '[' {
		t, rest, err := getDomainLiteral(v)
		if err != nil {
			return nil, v, err
		}
		if leader != nil {
			t.prepend(leader)
		}
		d.append(t)
		return d, rest, nil
	}
	t, rest, err := getDotAtom(v)
	if err != nil {
		if errors.Is(err, errFatal) {
			return nil, v, err
		}
		t, rest, err = getAtom(v)
		if err != nil {
			return nil, v, err
		}
	}
	v = rest
	if len(v) > 0 && v[0] == '@' {
		return nil, v, errHeaderParse
	}
	if leader != nil {
		t.prepend(leader)
	}
	d.append(t)
	if len(v) > 0 && v[0] == '.' {
		d.addDefect(false)
		if d.kids[0].typ == "dot-atom" {
			d.kids = slices.Clone(d.kids[0].kids)
		}
		for len(v) > 0 && v[0] == '.' {
			d.append(dotToken)
			t, rest, err := getAtom(v[1:])
			if err != nil {
				return nil, v, err
			}
			d.append(t)
			v = rest
		}
	}
	return d, v, nil
}

func getAddrSpec(v []rune) (*token, []rune, error) {
	as := list(cAddrSpec, "addr-spec")
	t, v, err := getLocalPart(v)
	if err != nil {
		return nil, v, err
	}
	as.append(t)
	if len(v) == 0 || v[0] != '@' {
		as.addDefect(true)
		return as, v, nil
	}
	as.append(vterm([]rune("@"), "address-at-symbol"))
	t, v, err = getDomain(v[1:])
	if err != nil {
		return nil, v, err
	}
	as.append(t)
	return as, v, nil
}

var (
	listSeparator        = &token{class: cTermValue, typ: "list-separator", text: []rune{','}}
	routeComponentMarker = &token{class: cTermValue, typ: "route-component-marker", text: []rune{'@'}}
)

func getObsRoute(v []rune) (*token, []rune, error) {
	or := list(cList, "obs-route")
	for len(v) > 0 && (v[0] == ',' || inSet(cfwsLeader, v[0])) {
		if inSet(cfwsLeader, v[0]) {
			t, rest, err := getCFWS(v)
			if err != nil {
				return nil, v, err
			}
			or.append(t)
			v = rest
		} else {
			or.append(listSeparator)
			v = v[1:]
		}
	}
	if len(v) == 0 || v[0] != '@' {
		return nil, v, errHeaderParse
	}
	or.append(routeComponentMarker)
	t, v, err := getDomain(v[1:])
	if err != nil {
		return nil, v, err
	}
	or.append(t)
	for len(v) > 0 && v[0] == ',' {
		or.append(listSeparator)
		v = v[1:]
		if len(v) == 0 {
			break
		}
		if inSet(cfwsLeader, v[0]) {
			t, rest, err := getCFWS(v)
			if err != nil {
				return nil, v, err
			}
			or.append(t)
			v = rest
		}
		if len(v) == 0 {
			break
		}
		if v[0] == '@' {
			or.append(routeComponentMarker)
			t, rest, err := getDomain(v[1:])
			if err != nil {
				return nil, v, err
			}
			or.append(t)
			v = rest
		}
	}
	if len(v) == 0 || v[0] != ':' {
		return nil, v, errHeaderParse
	}
	or.append(vterm([]rune(":"), "end-of-obs-route-marker"))
	return or, v[1:], nil
}

func getAngleAddr(v []rune) (*token, []rune, error) {
	aa := list(cList, "angle-addr")
	if first(v, cfwsLeader) {
		t, rest, err := getCFWS(v)
		if err != nil {
			return nil, v, err
		}
		aa.append(t)
		v = rest
	}
	if len(v) == 0 || v[0] != '<' {
		return nil, v, errHeaderParse
	}
	aa.append(vterm([]rune("<"), "angle-addr-start"))
	v = v[1:]
	if len(v) > 0 && v[0] == '>' {
		aa.append(vterm([]rune(">"), "angle-addr-end"))
		aa.addDefect(true)
		return aa, v[1:], nil
	}
	t, rest, err := getAddrSpec(v)
	if err != nil {
		if errors.Is(err, errFatal) {
			return nil, v, err
		}
		route, rest, err := getObsRoute(v)
		if err != nil {
			if errors.Is(err, errFatal) {
				return nil, v, err
			}
			return nil, v, errHeaderParse
		}
		aa.addDefect(false)
		aa.append(route)
		t, rest, err = getAddrSpec(rest)
		if err != nil {
			return nil, v, err
		}
		v = rest
	} else {
		v = rest
	}
	aa.append(t)
	if len(v) > 0 && v[0] == '>' {
		v = v[1:]
	} else {
		aa.addDefect(true)
	}
	aa.append(vterm([]rune(">"), "angle-addr-end"))
	if first(v, cfwsLeader) {
		t, rest, err := getCFWS(v)
		if err != nil {
			return nil, v, err
		}
		aa.append(t)
		v = rest
	}
	return aa, v, nil
}

func getDisplayName(v []rune) (*token, []rune, error) {
	p, rest, err := getPhrase(v)
	if err != nil {
		return nil, v, err
	}
	dn := list(cDisplayName, "display-name", p.kids...)
	dn.defects, dn.invalid = p.defects, p.invalid
	return dn, rest, nil
}

func getNameAddr(v []rune) (*token, []rune, error) {
	na := list(cList, "name-addr")
	var leader *token
	if len(v) == 0 {
		return nil, v, errHeaderParse
	}
	if inSet(cfwsLeader, v[0]) {
		var err error
		leader, v, err = getCFWS(v)
		if err != nil {
			return nil, v, err
		}
		if len(v) == 0 {
			return nil, v, errHeaderParse
		}
	}
	if v[0] != '<' {
		if inSet(phraseEnds, v[0]) {
			return nil, v, errHeaderParse
		}
		t, rest, err := getDisplayName(v)
		if err != nil {
			return nil, v, err
		}
		v = rest
		if len(v) == 0 {
			return nil, v, errHeaderParse
		}
		if leader != nil {
			if len(t.kids) > 0 && !t.kids[0].isTerm() {
				t.kids[0].prepend(leader)
			} else {
				t.prepend(leader)
			}
			leader = nil
		}
		na.append(t)
	}
	t, rest, err := getAngleAddr(v)
	if err != nil {
		return nil, v, err
	}
	if leader != nil {
		t.prepend(leader)
	}
	na.append(t)
	return na, rest, nil
}

func getMailbox(v []rune) (*token, []rune, error) {
	mb := list(cList, "mailbox")
	mb.mailbox = true
	t, rest, err := getNameAddr(v)
	if err != nil {
		if errors.Is(err, errFatal) {
			return nil, v, err
		}
		t, rest, err = getAddrSpec(v)
		if err != nil {
			if errors.Is(err, errFatal) {
				return nil, v, err
			}
			return nil, v, errHeaderParse
		}
	}
	if t.allInvalid() {
		mb.typ = "invalid-mailbox"
	}
	mb.append(t)
	return mb, rest, nil
}

func getInvalidMailbox(v []rune, ends string) (*token, []rune, error) {
	im := list(cList, "invalid-mailbox")
	for len(v) > 0 && !inSet(ends, v[0]) {
		if inSet(phraseEnds, v[0]) {
			im.append(vterm(v[:1], "misplaced-special"))
			v = v[1:]
			continue
		}
		t, rest, err := getPhrase(v)
		if err != nil {
			return nil, v, err
		}
		im.append(t)
		v = rest
	}
	return im, v, nil
}

func getMailboxList(v []rune) (*token, []rune, error) {
	ml := list(cList, "mailbox-list")
	for len(v) > 0 && v[0] != ';' {
		t, rest, err := getMailbox(v)
		switch {
		case err == nil:
			ml.append(t)
			v = rest
		case errors.Is(err, errFatal):
			return nil, v, err
		default:
			var leader *token
			if inSet(cfwsLeader, v[0]) {
				leader, v, err = getCFWS(v)
				if err != nil {
					return nil, v, err
				}
				if len(v) == 0 || inSet(",;", v[0]) {
					ml.append(leader)
					ml.addDefect(false)
				} else {
					t, v, err = getInvalidMailbox(v, ",;")
					if err != nil {
						return nil, v, err
					}
					t.prepend(leader)
					ml.append(t)
					ml.addDefect(true)
				}
			} else if v[0] == ',' {
				ml.addDefect(false)
			} else {
				t, v, err = getInvalidMailbox(v, ",;")
				if err != nil {
					return nil, v, err
				}
				ml.append(t)
				ml.addDefect(true)
			}
		}
		if len(v) > 0 && !inSet(",;", v[0]) {
			mb := ml.last()
			mb.typ = "invalid-mailbox"
			t, rest, err := getInvalidMailbox(v, ",;")
			if err != nil {
				return nil, v, err
			}
			mb.append(t.kids...)
			v = rest
			ml.addDefect(true)
		}
		if len(v) > 0 && v[0] == ',' {
			ml.append(listSeparator)
			v = v[1:]
		}
	}
	return ml, v, nil
}

func getGroupList(v []rune) (*token, []rune, error) {
	gl := list(cList, "group-list")
	if len(v) == 0 {
		gl.addDefect(true)
		return gl, v, nil
	}
	var leader *token
	if inSet(cfwsLeader, v[0]) {
		var err error
		leader, v, err = getCFWS(v)
		if err != nil {
			return nil, v, err
		}
		if len(v) == 0 {
			gl.addDefect(true)
			gl.append(leader)
			return gl, v, nil
		}
		if v[0] == ';' {
			gl.append(leader)
			return gl, v, nil
		}
	}
	t, v, err := getMailboxList(v)
	if err != nil {
		return nil, v, err
	}
	if len(t.allMailboxes()) == 0 {
		if leader != nil {
			gl.append(leader)
		}
		gl.append(t.kids...)
		gl.addDefect(false)
		return gl, v, nil
	}
	if leader != nil {
		t.prepend(leader)
	}
	gl.append(t)
	return gl, v, nil
}

func getGroup(v []rune) (*token, []rune, error) {
	g := list(cList, "group")
	t, v, err := getDisplayName(v)
	if err != nil {
		return nil, v, err
	}
	if len(v) == 0 || v[0] != ':' {
		return nil, v, errHeaderParse
	}
	g.append(t, vterm([]rune(":"), "group-display-name-terminator"))
	v = v[1:]
	if len(v) > 0 && v[0] == ';' {
		g.append(vterm([]rune(";"), "group-terminator"))
		return g, v[1:], nil
	}
	t, v, err = getGroupList(v)
	if err != nil {
		return nil, v, err
	}
	g.append(t)
	if len(v) == 0 {
		g.addDefect(true)
	} else if v[0] != ';' {
		return nil, v, errHeaderParse
	}
	g.append(vterm([]rune(";"), "group-terminator"))
	if len(v) > 0 {
		v = v[1:]
	}
	if first(v, cfwsLeader) {
		t, rest, err := getCFWS(v)
		if err != nil {
			return nil, v, err
		}
		g.append(t)
		v = rest
	}
	return g, v, nil
}

func getAddress(v []rune) (*token, []rune, error) {
	a := list(cList, "address")
	t, rest, err := getGroup(v)
	if err != nil {
		if errors.Is(err, errFatal) {
			return nil, v, err
		}
		t, rest, err = getMailbox(v)
		if err != nil {
			return nil, v, err
		}
	}
	a.append(t)
	return a, rest, nil
}

func getAddressList(v []rune) (*token, error) {
	al := list(cList, "address-list")
	for len(v) > 0 {
		t, rest, err := getAddress(v)
		switch {
		case err == nil:
			al.append(t)
			v = rest
		case errors.Is(err, errFatal):
			return nil, err
		default:
			var leader *token
			if inSet(cfwsLeader, v[0]) {
				leader, v, err = getCFWS(v)
				if err != nil {
					return nil, err
				}
				if len(v) == 0 || v[0] == ',' {
					al.append(leader)
					al.addDefect(false)
				} else {
					t, v, err = getInvalidMailbox(v, ",")
					if err != nil {
						return nil, err
					}
					t.prepend(leader)
					al.append(list(cList, "address", t))
					al.addDefect(true)
				}
			} else if v[0] == ',' {
				al.addDefect(false)
			} else {
				t, v, err = getInvalidMailbox(v, ",")
				if err != nil {
					return nil, err
				}
				al.append(list(cList, "address", t))
				al.addDefect(true)
			}
		}
		if len(v) > 0 && v[0] != ',' {
			t, rest, err := getInvalidMailbox(v, ",")
			if err != nil {
				return nil, err
			}
			al.append(list(cList, "address", t))
			v = rest
			al.addDefect(true)
		}
		if len(v) > 0 {
			al.append(listSeparator)
			v = v[1:]
		}
	}
	return al, nil
}

func getNoFoldLiteral(v []rune) (*token, []rune, error) {
	if len(v) == 0 || v[0] != '[' {
		return nil, v, errHeaderParse
	}
	nfl := list(cList, "no-fold-literal", vterm([]rune("["), "no-fold-literal-start"))
	t, rest := getDtext(v[1:])
	nfl.append(t)
	if len(rest) == 0 || rest[0] != ']' {
		return nil, v, errHeaderParse
	}
	nfl.append(vterm([]rune("]"), "no-fold-literal-end"))
	return nfl, rest[1:], nil
}

func getMsgID(v []rune) (*token, []rune, error) {
	m := list(cList, "msg-id")
	if first(v, cfwsLeader) {
		t, rest, err := getCFWS(v)
		if err != nil {
			return nil, v, err
		}
		m.append(t)
		v = rest
	}
	if len(v) == 0 || v[0] != '<' {
		return nil, v, errHeaderParse
	}
	m.append(vterm([]rune("<"), "msg-id-start"))
	v = v[1:]
	t, rest, err := getDotAtomText(v)
	if err != nil {
		t, rest, err = getObsLocalPart(v)
		if err != nil {
			if errors.Is(err, errFatal) {
				return nil, v, err
			}
			return nil, v, errHeaderParse
		}
	}
	v = rest
	m.append(t)
	if len(v) == 0 || v[0] != '@' {
		if len(v) > 0 && v[0] == '>' {
			m.append(vterm([]rune(">"), "msg-id-end"))
			v = v[1:]
		}
		return m, v, nil
	}
	m.append(vterm([]rune("@"), "address-at-symbol"))
	v = v[1:]
	t, rest, err = getDotAtomText(v)
	if err != nil {
		t, rest, err = getNoFoldLiteral(v)
		if err != nil {
			t, rest, err = getDomain(v)
			if err != nil {
				if errors.Is(err, errFatal) {
					return nil, v, err
				}
				return nil, v, errHeaderParse
			}
		}
	}
	v = rest
	m.append(t)
	if len(v) > 0 && v[0] == '>' {
		v = v[1:]
	}
	m.append(vterm([]rune(">"), "msg-id-end"))
	if first(v, cfwsLeader) {
		t, rest, err := getCFWS(v)
		if err != nil {
			return nil, v, err
		}
		m.append(t)
		v = rest
	}
	return m, v, nil
}

func parseMessageID(v []rune) (*token, error) {
	t, _, err := getMsgID(v)
	switch {
	case err == nil:
		return list(cList, "message-id", t), nil
	case errors.Is(err, errFatal):
		return nil, err
	}
	return list(cList, "invalid-message-id", getUnstructured(v).kids...), nil
}

func parseMessageIDs(v []rune) (*token, error) {
	l := list(cList, "message-id-list")
	for len(v) > 0 {
		if v[0] == ',' {
			l.append(wsterm([]rune(" "), "invalid-comma-replacement"))
			v = v[1:]
			continue
		}
		t, rest, err := getMsgID(v)
		switch {
		case err == nil:
			l.append(t)
			v = rest
			continue
		case errors.Is(err, errFatal):
			return nil, err
		}
		l.append(list(cList, "invalid-message-id", getUnstructured(v).kids...))
		break
	}
	return l, nil
}

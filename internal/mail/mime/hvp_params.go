package mime

import (
	"errors"
	"slices"
	"sort"
)

func getInvalidParameter(v []rune) (*token, []rune, error) {
	ip := list(cParam, "invalid-parameter")
	for len(v) > 0 && v[0] != ';' {
		if inSet(phraseEnds, v[0]) {
			ip.append(vterm(v[:1], "misplaced-special"))
			v = v[1:]
			continue
		}
		t, rest, err := getPhrase(v)
		if err != nil {
			return nil, v, err
		}
		ip.append(t)
		v = rest
	}
	return ip, v, nil
}

func getToken(v []rune) (*token, []rune, error) {
	mt := list(cList, "token")
	if first(v, cfwsLeader) {
		t, rest, err := getCFWS(v)
		if err != nil {
			return nil, v, err
		}
		mt.append(t)
		v = rest
	}
	if first(v, tokenEnds) {
		return nil, v, errHeaderParse
	}
	n := runUntil(v, tokenEnds)
	if n == 0 {
		return nil, v, errHeaderParse
	}
	mt.append(vterm(v[:n], "ttext"))
	v = v[n:]
	if first(v, cfwsLeader) {
		t, rest, err := getCFWS(v)
		if err != nil {
			return nil, v, err
		}
		mt.append(t)
		v = rest
	}
	return mt, v, nil
}

func getAttrtext(v []rune, ends, typ string) (*token, []rune, error) {
	n := runUntil(v, ends)
	if n == 0 {
		return nil, v, errHeaderParse
	}
	return vterm(v[:n], typ), v[n:], nil
}

func getAttribute(v []rune, ends, typ string) (*token, []rune, error) {
	a := list(cAttribute, "attribute")
	if first(v, cfwsLeader) {
		t, rest, err := getCFWS(v)
		if err != nil {
			return nil, v, err
		}
		a.append(t)
		v = rest
	}
	if first(v, ends) {
		return nil, v, errHeaderParse
	}
	t, v, err := getAttrtext(v, ends, typ)
	if err != nil {
		return nil, v, err
	}
	a.append(t)
	if first(v, cfwsLeader) {
		t, rest, err := getCFWS(v)
		if err != nil {
			return nil, v, err
		}
		a.append(t)
		v = rest
	}
	return a, v, nil
}

func getSection(v []rune) (*token, []rune, error) {
	if len(v) == 0 || v[0] != '*' {
		return nil, v, errHeaderParse
	}
	s := list(cList, "section", vterm([]rune("*"), "section-marker"))
	v = v[1:]
	if len(v) == 0 || !isDigit(v[0]) {
		return nil, v, errHeaderParse
	}
	n := 0
	for n < len(v) && isDigit(v[n]) {
		n++
	}
	num, ok := pyInt(v[:n])
	if !ok {
		return nil, v, errFatal
	}
	s.number = int(num)
	s.append(vterm(v[:n], "digits"))
	return s, v[n:], nil
}

func getValue(v []rune) (*token, []rune, error) {
	val := list(cValue, "value")
	if len(v) == 0 {
		return nil, v, errHeaderParse
	}
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
	if v[0] == '"' {
		t, v, err = getQuotedString(v)
	} else {
		t, v, err = getAttribute(v, extAttrEnds, "extended-attrtext")
	}
	if err != nil {
		return nil, v, err
	}
	if leader != nil {
		t.prepend(leader)
	}
	val.append(t)
	return val, v, nil
}

func (t *token) strippedValue() []rune {
	switch t.class {
	case cValue:
		k := firstKid(t)
		if k.typ == "cfws" {
			if len(t.kids) < 2 {
				panic(fatal{})
			}
			k = t.kids[1]
		}
		switch k.typ {
		case "quoted-string", "bare-quoted-string", "attribute", "extended-attribute":
			return k.strippedValue()
		}
		return t.joinValue()
	case cAttribute:
		for _, k := range t.kids {
			if len(k.typ) >= 8 && k.typ[len(k.typ)-8:] == "attrtext" {
				return k.value()
			}
		}
		return nil
	case cQuoted, cBareQuoted:
		for _, k := range t.kids {
			if k.typ == "bare-quoted-string" {
				return k.value()
			}
		}
		if t.class == cBareQuoted {
			return nil
		}
		return nil
	}
	return nil
}

func (t *token) paramValue() []rune {
	for _, k := range t.kids {
		if k.typ == "value" {
			return k.strippedValue()
		}
		if k.typ == "quoted-string" {
			for _, q := range k.kids {
				if q.typ == "bare-quoted-string" {
					for _, b := range q.kids {
						if b.typ == "value" {
							return b.strippedValue()
						}
					}
				}
			}
		}
	}
	return nil
}

func getParameter(v []rune) (*token, []rune, error) {
	p := list(cParam, "parameter")
	t, v, err := getAttribute(v, attributeEnds, "attrtext")
	if err != nil {
		return nil, v, err
	}
	p.append(t)
	if len(v) == 0 || v[0] == ';' {
		p.addDefect(true)
		return p, v, nil
	}
	if v[0] == '*' {
		if s, rest, err := getSection(v); err == nil {
			p.sectioned = true
			p.number = s.number
			p.append(s)
			v = rest
		} else if errors.Is(err, errFatal) {
			return nil, v, err
		}
		if len(v) == 0 {
			return nil, v, errHeaderParse
		}
		if v[0] == '*' {
			p.append(vterm([]rune("*"), "extended-parameter-marker"))
			v = v[1:]
			p.extended = true
		}
	}
	if len(v) == 0 {
		return nil, v, errFatal
	}
	if v[0] != '=' {
		return nil, v, errHeaderParse
	}
	p.append(vterm([]rune("="), "parameter-separator"))
	v = v[1:]
	if first(v, cfwsLeader) {
		t, rest, err := getCFWS(v)
		if err != nil {
			return nil, v, err
		}
		p.append(t)
		v = rest
	}
	var remainder []rune
	hasRemainder := false
	appendTo := p
	if p.extended && len(v) > 0 && v[0] == '"' {
		qs, rem, err := getQuotedString(v)
		if err != nil {
			return nil, v, err
		}
		inner := qs.strippedValue()
		semiValid := false
		if p.sectionNumber() == 0 {
			if len(inner) > 0 && inner[0] == '\'' {
				semiValid = true
			} else {
				_, rest, err := getAttrtext(inner, attributeEnds, "attrtext")
				if err != nil {
					return nil, v, err
				}
				if len(rest) > 0 && rest[0] == '\'' {
					semiValid = true
				}
			}
		} else if _, rest, err := getAttrtext(inner, extAttrEnds, "extended-attrtext"); err == nil && len(rest) == 0 {
			semiValid = true
		}
		if semiValid {
			p.addDefect(true)
			p.append(qs)
			for _, k := range qs.kids {
				if k.typ == "bare-quoted-string" {
					k.kids = nil
					appendTo = k
					break
				}
			}
			v = inner
			remainder, hasRemainder = rem, true
		} else {
			p.addDefect(true)
		}
	}
	var tok *token
	if len(v) == 0 || v[0] != '\'' {
		tok, v, err = getValue(v)
		if err != nil {
			return nil, v, err
		}
	}
	if !p.extended || p.sectionNumber() > 0 {
		if len(v) == 0 || v[0] != '\'' {
			appendTo.append(tok)
			if hasRemainder {
				v = remainder
			}
			return p, v, nil
		}
		p.addDefect(true)
	}
	if len(v) == 0 {
		p.addDefect(true)
		appendTo.append(tok)
		if !hasRemainder {
			return p, v, nil
		}
	} else {
		if tok != nil {
			var t *token
			for _, t = range tok.kids {
				if t.typ == "extended-attrtext" {
					break
				}
			}
			appendTo.append(t)
			p.charset = t.value()
		}
		if v[0] != '\'' {
			return nil, v, errHeaderParse
		}
		appendTo.append(vterm([]rune("'"), "RFC2231-delimiter"))
		v = v[1:]
		if len(v) > 0 && v[0] != '\'' {
			t, rest, err := getAttrtext(v, attributeEnds, "attrtext")
			if err != nil {
				return nil, v, err
			}
			appendTo.append(t)
			v = rest
			if len(v) == 0 || v[0] != '\'' {
				return nil, v, errHeaderParse
			}
		}
		appendTo.append(vterm([]rune("'"), "RFC2231-delimiter"))
		if len(v) > 0 {
			v = v[1:]
		}
	}
	if hasRemainder {
		val := list(cValue, "value")
		for len(v) > 0 {
			var t *token
			switch {
			case inSet(wsp, v[0]):
				t, v = getFWS(v)
			case v[0] == '"':
				t = vterm([]rune(`"`), "DQUOTE")
				v = v[1:]
			default:
				t, v = getQContent(v)
			}
			val.append(t)
		}
		tok = val
	} else {
		tok, v, err = getValue(v)
		if err != nil {
			return nil, v, err
		}
	}
	appendTo.append(tok)
	if hasRemainder {
		v = remainder
	}
	return p, v, nil
}

func (t *token) sectionNumber() int {
	if t.sectioned {
		return t.number
	}
	return 0
}

func parseMimeParameters(v []rune) (*token, error) {
	mp := list(cMimeParams, "mime-parameters")
	for len(v) > 0 {
		t, rest, err := getParameter(v)
		switch {
		case err == nil:
			mp.append(t)
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
			}
			if len(v) == 0 {
				mp.append(leader)
				return mp, nil
			}
			if v[0] == ';' {
				if leader != nil {
					mp.append(leader)
				}
				mp.addDefect(true)
			} else {
				t, v, err = getInvalidParameter(v)
				if err != nil {
					return nil, err
				}
				if leader != nil && len(leader.kids) > 0 {
					t.prepend(leader)
				}
				mp.append(t)
				mp.addDefect(true)
			}
		}
		if len(v) > 0 && v[0] != ';' {
			p := mp.last()
			p.typ = "invalid-parameter"
			t, rest, err := getInvalidParameter(v)
			if err != nil {
				return nil, err
			}
			p.append(t.kids...)
			v = rest
			mp.addDefect(true)
		}
		if len(v) > 0 {
			mp.append(vterm([]rune(";"), "parameter-separator"))
			v = v[1:]
		}
	}
	return mp, nil
}

type param struct {
	name, value []rune
}

func (t *token) params() []param {
	type part struct {
		section int
		tok     *token
	}
	var order [][]rune
	groups := map[string][]part{}
	for _, k := range t.kids {
		if len(k.typ) < 9 || k.typ[len(k.typ)-9:] != "parameter" {
			continue
		}
		if len(k.kids) == 0 || k.kids[0].typ != "attribute" {
			continue
		}
		name := strip(k.kids[0].value())
		key := string(name)
		if _, ok := groups[key]; !ok {
			order = append(order, name)
		}
		groups[key] = append(groups[key], part{k.sectionNumber(), k})
	}
	var out []param
	for _, name := range order {
		parts := groups[string(name)]
		sort.SliceStable(parts, func(i, j int) bool { return parts[i].section < parts[j].section })
		firstParam := parts[0].tok
		charset := firstParam.charset
		if charset == nil {
			charset = []rune("us-ascii")
		}
		if !firstParam.extended && len(parts) > 1 && parts[1].section == 0 {
			parts = parts[:1]
		}
		var value []rune
		i := 0
		for _, pt := range parts {
			if pt.section != i && !pt.tok.extended {
				continue
			}
			i++
			pv := pt.tok.paramValue()
			if pt.tok.extended {
				pv = decodeExtended(pv, charset)
			}
			value = append(value, pv...)
		}
		out = append(out, param{name, value})
	}
	return out
}

func decodeExtended(v []rune, charset []rune) []rune {
	if hasSurrogates(v) {
		out := make([]rune, 0, len(v))
		for i := 0; i < len(v); i++ {
			if v[i] == '%' && i+2 < len(v) && v[i+1] < 0x80 && v[i+2] < 0x80 && isHex(byte(v[i+1])) && isHex(byte(v[i+2])) {
				out = append(out, rune(hexValue(byte(v[i+1]))<<4|hexValue(byte(v[i+2]))))
				i += 2
				continue
			}
			out = append(out, v[i])
		}
		return out
	}
	raw := percentUnquote([]byte(string(v)))
	if hasSurrogates(charset) {
		out, _ := decodeASCII(raw, errSurrogateEscape)
		return out
	}
	f, err := lookupCodec(string(charset))
	if errors.Is(err, errNul) {
		panic(fatal{})
	}
	if err != nil {
		out, _ := decodeASCII(raw, errSurrogateEscape)
		return out
	}
	out, err := f(raw, errSurrogateEscape)
	if err != nil {
		panic(fatal{})
	}
	return out
}

type fatal struct{}

func percentUnquote(b []byte) []byte {
	out := make([]byte, 0, len(b))
	for i := 0; i < len(b); i++ {
		if b[i] == '%' && i+2 < len(b) && isHex(b[i+1]) && isHex(b[i+2]) {
			out = append(out, hexValue(b[i+1])<<4|hexValue(b[i+2]))
			i += 2
			continue
		}
		out = append(out, b[i])
	}
	return out
}

func (t *token) mimeParamsStr() []rune {
	var out []rune
	for i, p := range t.params() {
		if i > 0 {
			out = append(out, ';', ' ')
		}
		out = append(out, p.name...)
		if len(p.value) > 0 {
			out = append(out, '=')
			out = append(out, quoteString(p.value)...)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return append([]rune{' '}, out...)
}

func findMimeParameters(tl *token, v []rune) error {
	for len(v) > 0 && v[0] != ';' {
		if inSet(phraseEnds, v[0]) {
			tl.append(vterm(v[:1], "misplaced-special"))
			v = v[1:]
			continue
		}
		t, rest, err := getPhrase(v)
		if err != nil {
			return err
		}
		tl.append(t)
		v = rest
	}
	if len(v) == 0 {
		return nil
	}
	tl.append(vterm([]rune(";"), "parameter-separator"))
	mp, err := parseMimeParameters(v[1:])
	if err != nil {
		return err
	}
	tl.append(mp)
	return nil
}

func parseContentType(v []rune) (*token, error) {
	ct := list(cList, "content-type")
	if len(v) == 0 {
		return ct, nil
	}
	t, rest, err := getToken(v)
	if err != nil {
		if errors.Is(err, errFatal) {
			return nil, err
		}
		return ct, findMimeParameters(ct, v)
	}
	ct.append(t)
	v = rest
	if len(v) == 0 || v[0] != '/' {
		if len(v) > 0 {
			return ct, findMimeParameters(ct, v)
		}
		return ct, nil
	}
	ct.append(vterm([]rune("/"), "content-type-separator"))
	v = v[1:]
	t, rest, err = getToken(v)
	if err != nil {
		if errors.Is(err, errFatal) {
			return nil, err
		}
		return ct, findMimeParameters(ct, v)
	}
	ct.append(t)
	v = rest
	if len(v) == 0 {
		return ct, nil
	}
	if v[0] != ';' {
		return ct, findMimeParameters(ct, v)
	}
	ct.append(vterm([]rune(";"), "parameter-separator"))
	mp, err := parseMimeParameters(v[1:])
	if err != nil {
		return nil, err
	}
	ct.append(mp)
	return ct, nil
}

func parseContentDisposition(v []rune) (*token, error) {
	cd := list(cList, "content-disposition")
	if len(v) == 0 {
		return cd, nil
	}
	t, rest, err := getToken(v)
	if err != nil {
		if errors.Is(err, errFatal) {
			return nil, err
		}
		return cd, findMimeParameters(cd, v)
	}
	cd.append(t)
	cd.disposition, cd.hasDisposition = lowerRunes(strip(t.value())), true
	v = rest
	if len(v) == 0 {
		return cd, nil
	}
	if v[0] != ';' {
		return cd, findMimeParameters(cd, v)
	}
	cd.append(vterm([]rune(";"), "parameter-separator"))
	mp, err := parseMimeParameters(v[1:])
	if err != nil {
		return nil, err
	}
	cd.append(mp)
	return cd, nil
}

func parseCTE(v []rune) (*token, error) {
	c := list(cList, "content-transfer-encoding")
	if len(v) == 0 {
		return c, nil
	}
	t, rest, err := getToken(v)
	switch {
	case err == nil:
		c.append(t)
		v = rest
	case errors.Is(err, errFatal):
		return nil, err
	}
	for len(v) > 0 {
		if inSet(phraseEnds, v[0]) {
			c.append(vterm(v[:1], "misplaced-special"))
			v = v[1:]
			continue
		}
		t, rest, err := getPhrase(v)
		if err != nil {
			return nil, err
		}
		c.append(t)
		v = rest
	}
	return c, nil
}

func parseMimeVersion(v []rune) (*token, error) {
	mv := list(cList, "mime-version")
	if len(v) == 0 {
		return mv, nil
	}
	if inSet(cfwsLeader, v[0]) {
		t, rest, err := getCFWS(v)
		if err != nil {
			return nil, err
		}
		mv.append(t)
		v = rest
	}
	digits := func(stopDot bool) ([]rune, error) {
		n := 0
		for n < len(v) && (!stopDot || v[n] != '.') && !inSet(cfwsLeader, v[n]) {
			n++
		}
		d := v[:n]
		v = v[n:]
		all := len(d) > 0
		for _, r := range d {
			all = all && isDigit(r)
		}
		if all {
			if _, ok := pyInt(d); !ok {
				return nil, errFatal
			}
		}
		return d, nil
	}
	cfws := func() error {
		if first(v, cfwsLeader) {
			t, rest, err := getCFWS(v)
			if err != nil {
				return err
			}
			mv.append(t)
			v = rest
		}
		return nil
	}
	d, err := digits(true)
	if err != nil {
		return nil, err
	}
	mv.append(vterm(d, "digits"))
	if err := cfws(); err != nil {
		return nil, err
	}
	if len(v) == 0 || v[0] != '.' {
		if len(v) > 0 {
			mv.append(vterm(v, "xtext"))
		}
		return mv, nil
	}
	mv.append(vterm([]rune("."), "version-separator"))
	v = v[1:]
	if err := cfws(); err != nil {
		return nil, err
	}
	if len(v) == 0 {
		return mv, nil
	}
	d, err = digits(false)
	if err != nil {
		return nil, err
	}
	mv.append(vterm(d, "digits"))
	if err := cfws(); err != nil {
		return nil, err
	}
	if len(v) > 0 {
		mv.append(vterm(slices.Clone(v), "xtext"))
	}
	return mv, nil
}

package mime

import (
	"encoding/base64"
	"fmt"
	"strings"
)

const foldMaxLen = 78

// FoldHeader is how email.policy.default writes a header set from a str: the value parsed by its header class
// (address, unstructured, date, message-id, references or MIME header) and refolded to 78 columns, joined with
// linesep and ending with it. utf8 is the policy's utf8 flag, which leaves non-ASCII text unencoded.
func FoldHeader(name, value, linesep string, utf8 bool) (folded string, err error) {
	defer guard(&err)
	v := []rune(value)
	var tree *token
	switch headerKind([]rune(name)) {
	case "address":
		tree, err = getAddressList(v)
	case "mime-version":
		tree, err = parseMimeVersion(v)
	case "content-type":
		tree, err = parseContentType(v)
	case "content-disposition":
		tree, err = parseContentDisposition(v)
	case "cte":
		tree, err = parseCTE(v)
	case "message-id":
		tree, err = parseMessageID(v)
	case "references":
		tree, err = parseMessageIDs(v)
	default:
		tree = getUnstructured(v)
	}
	if err != nil {
		return "", err
	}
	header := list(cList, "header", list(cList, "header-label", vterm([]rune(name), "header-name"), vterm([]rune(":"), "header-sep")))
	if len(tree.kids) > 0 {
		header.append(list(cWSList, "cfws", wsterm([]rune(" "), "fws")))
	}
	header.append(tree)
	return refold(header, linesep, utf8), nil
}

func (t *token) asEWAllowed() bool {
	if t.isTerm() {
		return t.typ != "list-separator"
	}
	switch t.typ {
	case "domain", "addr-spec", "obs-local-part", "invalid-obs-local-part", "local-part", "domain-literal",
		"no-fold-literal", "content-type", "content-disposition", "content-transfer-encoding", "header-label",
		"msg-id", "message-id", "invalid-message-id":
		return false
	case "dot-atom-text":
		return true
	}
	for _, k := range t.kids {
		if !k.asEWAllowed() {
			return false
		}
	}
	return true
}

func (t *token) syntacticBreak() bool {
	switch t.typ {
	case "list-separator", "mime-parameters", "content-type", "content-disposition":
		return false
	}
	return true
}

func (t *token) ewCombineAllowed() bool { return t.isTerm() || t.typ != "display-name" }

func (t *token) startsWithFWS() bool {
	switch {
	case t.class == cTermWS || t.class == cTermEWWS:
		return true
	case t.isTerm():
		return false
	case len(t.kids) == 0:
		return false
	}
	return t.kids[0].startsWithFWS()
}

func (t *token) fold(linesep string, utf8 bool) string {
	switch t.typ {
	case "msg-id", "message-id", "invalid-message-id":
		return string(t.str()) + linesep
	}
	return refold(t, linesep, utf8)
}

var foldSpecialsNL = `()<>@,:;.\"[]` + "\r\n"

func wspTrimmed(s []rune) bool {
	for _, r := range s {
		if r != ' ' && r != '\t' {
			return false
		}
	}
	return true
}

func stealTrailingWSP(lines *[][]rune) []rune {
	ls := *lines
	if len(ls) == 0 {
		return nil
	}
	last := ls[len(ls)-1]
	if len(last) == 0 || (last[len(last)-1] != ' ' && last[len(last)-1] != '\t') {
		return nil
	}
	wsp := []rune{last[len(last)-1]}
	ls[len(ls)-1] = last[:len(last)-1]
	if len(ls[len(ls)-1]) == 0 {
		*lines = ls[:len(ls)-1]
	}
	return wsp
}

func runesASCII(s []rune) bool {
	for _, r := range s {
		if r >= 0x80 {
			return false
		}
	}
	return true
}

func makeQuotedPairs(s []rune) []rune {
	return []rune(strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(string(s)))
}

func makeParenthesisPairs(s []rune) []rune {
	return []rune(strings.NewReplacer(`\`, `\\`, `(`, `\(`, `)`, `\)`).Replace(string(s)))
}

var endEWNotAllowed = &token{class: cTermValue, typ: "wrap_as_ew_blocked"}

// refold is _header_value_parser._refold_parse_tree with max_line_length 78.
func refold(tree *token, linesep string, utf8 bool) string {
	maxlen := foldMaxLen
	encoding := "us-ascii"
	if utf8 {
		encoding = "utf-8"
	}
	lines := [][]rune{{}}
	lastWordIsEW := false
	lastEW := -1
	lastCharset := ""
	blocked := 0
	wantEncoding := false
	parts := append([]*token(nil), tree.kids...)
	for len(parts) > 0 {
		part := parts[0]
		parts = parts[1:]
		if part == endEWNotAllowed {
			blocked--
			continue
		}
		tstr := part.str()
		if !wantEncoding {
			if part.typ == "ptext" || part.typ == "vtext" {
				wantEncoding = strings.ContainsAny(string(tstr), foldSpecialsNL)
			} else {
				wantEncoding = strings.ContainsAny(string(tstr), "\r\n")
			}
		}
		charset := encoding
		if !utf8 && !runesASCII(tstr) {
			charset = "utf-8"
			wantEncoding = true
		}
		if part.typ == "mime-parameters" {
			foldMimeParameters(part, &lines, maxlen, encoding)
			lastWordIsEW = false
			continue
		}
		if wantEncoding && blocked == 0 {
			if !part.asEWAllowed() {
				wantEncoding = false
				lastEW = -1
				if part.syntacticBreak() {
					encoded := part.fold(linesep, utf8)
					encoded = encoded[:len(encoded)-len(linesep)]
					if !strings.Contains(encoded, linesep) {
						if len([]rune(encoded)) > maxlen-len(lines[len(lines)-1]) {
							newline := stealTrailingWSP(&lines)
							lines = append(lines, newline)
						}
						lines[len(lines)-1] = append(lines[len(lines)-1], []rune(encoded)...)
						lastWordIsEW = false
						continue
					}
				}
			}
			switch {
			case !part.isTerm():
				parts = append(append([]*token(nil), part.kids...), parts...)
				wantEncoding = false
				continue
			case part.asEWAllowed():
				if lastEW >= 0 && charset != lastCharset && (lastCharset == "unknown-8bit" || lastCharset == "utf-8" && charset != "us-ascii") {
					lastEW = -1
				}
				lastEW = foldAsEW(tstr, &lines, maxlen, lastEW, part.ewCombineAllowed(), charset, lastWordIsEW)
				lastWordIsEW = true
				lastCharset = charset
				wantEncoding = false
				continue
			default:
				lastEW = -1
				wantEncoding = false
			}
		}
		last := lines[len(lines)-1]
		if len(tstr) <= maxlen-len(last) {
			lines[len(lines)-1] = append(last, tstr...)
			lastWordIsEW = lastWordIsEW && wspTrimmed(tstr)
			continue
		}
		if part.syntacticBreak() && len(tstr)+1 <= maxlen {
			newline := stealTrailingWSP(&lines)
			if len(newline) > 0 || part.startsWithFWS() {
				lines = append(lines, append(newline, tstr...))
				lastWordIsEW = lastWordIsEW && wspTrimmed(lines[len(lines)-1])
				lastEW = -1
				continue
			}
		}
		if !part.isTerm() {
			newparts := append([]*token(nil), part.kids...)
			switch part.typ {
			case "bare-quoted-string":
				quoted := []*token{vterm([]rune(`"`), "ptext")}
				for _, p := range newparts {
					quoted = append(quoted, vterm(makeQuotedPairs(p.str()), "ptext"))
				}
				newparts = append(quoted, vterm([]rune(`"`), "ptext"))
			case "comment":
				paren := []*token{vterm([]rune("("), "ptext")}
				for _, p := range newparts {
					if p.typ == "ptext" {
						p = vterm(makeParenthesisPairs(p.str()), "ptext")
					}
					paren = append(paren, p)
				}
				newparts = append(paren, vterm([]rune(")"), "ptext"))
			}
			if !part.asEWAllowed() {
				blocked++
				newparts = append(newparts, endEWNotAllowed)
			}
			parts = append(newparts, parts...)
			continue
		}
		if part.asEWAllowed() && blocked == 0 {
			parts = append([]*token{part}, parts...)
			wantEncoding = true
			continue
		}
		newline := stealTrailingWSP(&lines)
		if len(newline) > 0 || part.startsWithFWS() {
			lines = append(lines, append(newline, tstr...))
		} else {
			lines[len(lines)-1] = append(lines[len(lines)-1], tstr...)
		}
		lastWordIsEW = lastWordIsEW && wspTrimmed(tstr)
	}
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = string(l)
	}
	return strings.Join(out, linesep) + linesep
}

// foldAsEW is _fold_as_ew; it returns the new last_ew, -1 for None.
func foldAsEW(toEncode []rune, linesp *[][]rune, maxlen, lastEW int, combine bool, charset string, lastWordIsEW bool) int {
	lines := *linesp
	defer func() { *linesp = lines }()
	cur := func() []rune { return lines[len(lines)-1] }
	switch {
	case lastEW >= 0 && combine:
		toEncode = getUnstructured(append(append([]rune(nil), cur()[lastEW:]...), toEncode...)).str()
		lines[len(lines)-1] = cur()[:lastEW]
	case lastWordIsEW:
		line := cur()
		n := len(line)
		for n > 0 && (line[n-1] == ' ' || line[n-1] == '\t') {
			n--
		}
		leading := append([]rune(nil), line[n:]...)
		line = line[:n]
		if len(leading) > 0 {
			line = append(line, ' ')
		}
		lines[len(lines)-1] = line
		toEncode = append(leading, toEncode...)
	case len(toEncode) > 0 && (toEncode[0] == ' ' || toEncode[0] == '\t'):
		lead := toEncode[0]
		toEncode = toEncode[1:]
		if len(cur()) == maxlen {
			newline := stealTrailingWSP(&lines)
			lines = append(lines, newline)
		}
		lines[len(lines)-1] = append(cur(), lead)
	}
	var trailing []rune
	if n := len(toEncode); n > 0 && (toEncode[n-1] == ' ' || toEncode[n-1] == '\t') {
		trailing = toEncode[n-1:]
		toEncode = toEncode[:n-1]
	}
	newLastEW := lastEW
	if lastEW < 0 {
		newLastEW = len(cur())
	}
	encodeAs := charset
	if charset == "us-ascii" {
		encodeAs = "utf-8"
	}
	chrome := len(encodeAs) + 7
	for len(toEncode) > 0 {
		remaining := maxlen - len(cur())
		textSpace := remaining - chrome
		if textSpace <= 0 {
			newline := stealTrailingWSP(&lines)
			if len(newline) == 0 {
				newline = []rune{' '}
			}
			lines = append(lines, newline)
			newLastEW = len(cur())
			continue
		}
		word := toEncode[:min(textSpace, len(toEncode))]
		encoded := encodeWord(word, encodeAs)
		for len(encoded)-remaining > 0 {
			word = word[:len(word)-1]
			encoded = encodeWord(word, encodeAs)
		}
		lines[len(lines)-1] = append(cur(), []rune(encoded)...)
		toEncode = toEncode[len(word):]
		if len(toEncode) > 0 {
			lines = append(lines, []rune{' '})
			newLastEW = len(cur())
		}
	}
	lines[len(lines)-1] = append(cur(), trailing...)
	if !combine {
		return -1
	}
	return newLastEW
}

func qByte(c byte) string {
	switch {
	case c == ' ':
		return "_"
	case c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("-!*+/", c) >= 0:
		return string(c)
	}
	return fmt.Sprintf("=%02X", c)
}

// encodeWord is _encoded_words.encode(word, charset): q unless b is at least 5 characters shorter.
func encodeWord(word []rune, charset string) string {
	b := []byte(string(word))
	var q strings.Builder
	for _, c := range b {
		q.WriteString(qByte(c))
	}
	blen := len(b) / 3 * 4
	if len(b)%3 != 0 {
		blen += 4
	}
	if q.Len()-blen < 5 {
		return "=?" + charset + "?q?" + q.String() + "?="
	}
	return "=?" + charset + "?b?" + base64.StdEncoding.EncodeToString(b) + "?="
}

func percentQuote(s []rune) string {
	var b strings.Builder
	for _, c := range []byte(string(s)) {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("_.-~", c) >= 0 {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// foldMimeParameters is _fold_mime_parameters: each parameter from its decoded value, RFC 2231 encoded when the
// value is not in the policy's encoding or does not fit.
func foldMimeParameters(part *token, linesp *[][]rune, maxlen int, encoding string) {
	lines := *linesp
	defer func() { *linesp = lines }()
	for _, p := range part.params() {
		if !strings.HasSuffix(strings.TrimRightFunc(string(lines[len(lines)-1]), isSpace), ";") {
			lines[len(lines)-1] = append(lines[len(lines)-1], ';')
		}
		charset := encoding
		required := encoding == "us-ascii" && !runesASCII(p.value)
		if required {
			charset = "utf-8"
		}
		var tstr []rune
		if required {
			tstr = []rune(string(p.name) + "*=" + charset + "''" + percentQuote(p.value))
		} else {
			tstr = append(append(append([]rune(nil), p.name...), '='), quoteString(p.value)...)
		}
		if len(lines[len(lines)-1])+len(tstr)+1 < maxlen {
			lines[len(lines)-1] = append(append(lines[len(lines)-1], ' '), tstr...)
			continue
		}
		if len(tstr)+2 <= maxlen {
			lines = append(lines, append([]rune{' '}, tstr...))
			continue
		}
		section := 0
		extra := charset + "''"
		value := p.value
		for len(value) > 0 {
			chrome := len(p.name) + len(fmt.Sprint(section)) + 3 + len(extra)
			if maxlen <= chrome+3 {
				maxlen = foldMaxLen
			}
			split := maxlen - chrome - 2
			maxchars := split
			var encoded string
			for {
				encoded = percentQuote(value[:min(split, len(value))])
				if len(encoded) <= maxchars {
					break
				}
				split--
			}
			lines = append(lines, []rune(fmt.Sprintf(" %s*%d*=%s%s", string(p.name), section, extra, encoded)))
			extra = ""
			section++
			value = value[min(split, len(value)):]
			if len(value) > 0 {
				lines[len(lines)-1] = append(lines[len(lines)-1], ';')
			}
		}
	}
}

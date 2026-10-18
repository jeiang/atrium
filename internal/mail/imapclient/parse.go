package imapclient

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/rx"
)

var (
	listLineRE    = rx.MustCompile(`\((?P<flags>[^)]*)\)\s+(?P<delim>"(?:[^"\\]|\\.)*"|NIL)\s+(?P<name>.+)$`, rx.Bytes|rx.DotAll)
	tokenRE       = rx.MustCompile(`\(|\)|"((?:[^"\\]|\\.)*)"|([^\s()"]+)`, 0)
	copyUIDRE     = rx.MustCompile(`COPYUID (\d+) ([\d:,]+) ([\d:,]+)`, 0)
	unquoteRE     = rx.MustCompile(`\\(.)`, 0)
	labelRE       = rx.MustCompile(`"((?:[^"\\]|\\.)*)"|(\S+)`, 0)
	flagsRE       = rx.MustCompile(`\b`+rx.Escape("FLAGS")+` \(`, 0)
	labelsRE      = rx.MustCompile(`\b`+rx.Escape("X-GM-LABELS")+` \(`, 0)
	structureRE   = rx.MustCompile(`BODYSTRUCTURE `, 0)
	groupStartRE  = rx.MustCompile(`\s*\d+ \(`, 0)
	sectionRE     = rx.MustCompile(`BODY\[(HEADER|TEXT)\]`, 0)
	uidRE         = rx.MustCompile(`\bUID (\d+)`, 0)
	modseqRE      = rx.MustCompile(`MODSEQ \((\d+)\)`, 0)
	sizeRE        = rx.MustCompile(`RFC822\.SIZE (\d+)`, 0)
	gmMsgIDRE     = rx.MustCompile(`X-GM-MSGID (\d+)`, 0)
	gmThrIDRE     = rx.MustCompile(`X-GM-THRID (\d+)`, 0)
	internalRE    = rx.MustCompile(`INTERNALDATE "([^"]+)"`, 0)
	uidValidityRE = rx.MustCompile(`UIDVALIDITY (\d+)`, 0)
	uidNextRE     = rx.MustCompile(`UIDNEXT (\d+)`, 0)
	highestRE     = rx.MustCompile(`HIGHESTMODSEQ (\d+)`, 0)
	messagesRE    = rx.MustCompile(`MESSAGES (\d+)`, 0)
)

var errSexp = errors.New("malformed BODYSTRUCTURE")

func unquote(token string) (string, error) { return unquoteRE.Sub(`\1`, token, 0) }

func expandUIDSet(text string) ([]uint32, error) {
	var uids []uint32
	for _, part := range strings.Split(text, ",") {
		if strings.Contains(part, ":") {
			bounds := strings.Split(part, ":")
			if len(bounds) != 2 {
				return nil, fmt.Errorf("too many values to unpack (expected 2, got %d)", len(bounds))
			}
			low, err := pyInt32(bounds[0])
			if err != nil {
				return nil, err
			}
			high, err := pyInt32(bounds[1])
			if err != nil {
				return nil, err
			}
			low, high = min(low, high), max(low, high)
			for u := uint64(low); u <= uint64(high); u++ {
				uids = append(uids, uint32(u))
			}
		} else if part != "" {
			u, err := pyInt32(part)
			if err != nil {
				return nil, err
			}
			uids = append(uids, u)
		}
	}
	return uids, nil
}

func pyInt32(s string) (uint32, error) {
	v, err := strconv.ParseUint(s, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("invalid literal for int() with base 10: %s", pyStrRepr(s))
	}
	return uint32(v), nil
}

func pyInt64(s string) (uint64, error) {
	v, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid literal for int() with base 10: %s", pyStrRepr(s))
	}
	return v, nil
}

func pyStrRepr(s string) string { return strings.TrimPrefix(bytesRepr([]byte(s)), "b") }

func joinData(data []datum, skipFalsy bool) string {
	parts := make([]string, 0, len(data))
	for _, d := range data {
		if skipFalsy && (d.none || (!d.tuple && len(d.text) == 0)) {
			continue
		}
		parts = append(parts, d.str())
	}
	return strings.Join(parts, " ")
}

func parseCopyUID(data []datum) (*mail.CopyUID, error) {
	m, err := copyUIDRE.Search(joinData(data, true))
	if err != nil || m == nil {
		return nil, err
	}
	validity, err := pyInt32(m.Group(1))
	if err != nil {
		return nil, err
	}
	src, err := expandUIDSet(m.Group(2))
	if err != nil {
		return nil, err
	}
	dst, err := expandUIDSet(m.Group(3))
	if err != nil {
		return nil, err
	}
	mapping := make(map[uint32]uint32, min(len(src), len(dst)))
	for i := range min(len(src), len(dst)) {
		mapping[src[i]] = dst[i]
	}
	return &mail.CopyUID{UIDValidity: validity, Mapping: mapping}, nil
}

func parseList(lines []datum) ([]mail.MailboxInfo, error) {
	found := []mail.MailboxInfo{}
	for _, line := range lines {
		if line.none || line.tuple {
			continue
		}
		m, err := listLineRE.Match(latin1(line.text))
		if err != nil {
			return nil, err
		}
		if m == nil {
			continue
		}
		name := pyStrip(m.Group(3))
		if strings.HasPrefix(name, `"`) && strings.HasSuffix(name, `"`) {
			r := []rune(name)
			if name, err = unquote(string(r[1:max(len(r)-1, 1)])); err != nil {
				return nil, err
			}
		}
		delim := m.Group(2)
		if delim == "NIL" {
			delim = ""
		} else {
			r := []rune(delim)
			if delim, err = unquote(string(r[1 : len(r)-1])); err != nil {
				return nil, err
			}
		}
		decoded, err := mail.UTF7Decode(name)
		if err != nil {
			return nil, err
		}
		found = append(found, mail.MailboxInfo{Name: decoded, IMAPName: name, Flags: pySplit(m.Group(1)), Delimiter: delim})
	}
	return found, nil
}

func parseLabels(text string) ([]string, error) {
	ms, err := labelRE.FindIter(text)
	if err != nil {
		return nil, err
	}
	labels := make([]string, 0, len(ms))
	for _, m := range ms {
		raw, quoted := m.GroupOK(1)
		if quoted {
			if raw, err = unquote(raw); err != nil {
				return nil, err
			}
		} else {
			raw = m.Group(2)
		}
		label, err := mail.UTF7Decode(raw)
		if err != nil {
			return nil, err
		}
		labels = append(labels, label)
	}
	return labels, nil
}

// balanced is imap._balanced over code points: the parenthesised block opening at start, or ok false.
func balanced(text []rune, start int) ([]rune, bool) {
	depth := 0
	inQuote := false
	for i := start; i < len(text); i++ {
		c := text[i]
		switch {
		case inQuote:
			switch c {
			case '\\':
				i++
			case '"':
				inQuote = false
			}
		case c == '"':
			inQuote = true
		case c == '(':
			depth++
		case c == ')':
			depth--
			if depth == 0 {
				return text[start : i+1], true
			}
		}
	}
	return nil, false
}

func section(meta string, re *rx.Regexp) (string, bool, error) {
	m, err := re.Search(meta)
	if err != nil || m == nil {
		return "", false, err
	}
	block, ok := balanced([]rune(meta), m.End()-1)
	if !ok {
		return "", false, nil
	}
	return string(block[1 : len(block)-1]), true, nil
}

type sexp struct {
	list   []sexp
	atom   string
	isList bool
}

func parseSexp(text string) (sexp, error) {
	ms, err := tokenRE.FindIter(text)
	if err != nil {
		return sexp{}, err
	}
	type token struct {
		paren string
		value string
	}
	tokens := make([]token, 0, len(ms))
	for _, m := range ms {
		g := m.Text()
		switch g {
		case "(", ")":
			tokens = append(tokens, token{paren: g})
		default:
			if s, ok := m.GroupOK(1); ok {
				v, err := unquote(s)
				if err != nil {
					return sexp{}, err
				}
				tokens = append(tokens, token{value: v})
			} else {
				tokens = append(tokens, token{value: g})
			}
		}
	}
	pos := 0
	var parse func() (sexp, error)
	parse = func() (sexp, error) {
		if pos >= len(tokens) {
			return sexp{}, errSexp
		}
		t := tokens[pos]
		pos++
		if t.paren == ")" {
			return sexp{}, errSexp
		}
		if t.paren != "(" {
			return sexp{atom: t.value}, nil
		}
		out := sexp{isList: true, list: []sexp{}}
		for {
			if pos >= len(tokens) {
				return sexp{}, errSexp
			}
			if tokens[pos].paren == ")" {
				break
			}
			child, err := parse()
			if err != nil {
				return sexp{}, err
			}
			out.list = append(out.list, child)
		}
		pos++
		return out, nil
	}
	return parse()
}

func flatten(node sexp, out []string) []string {
	if !node.isList {
		return append(out, node.atom)
	}
	for _, child := range node.list {
		out = flatten(child, out)
	}
	return out
}

func attachmentsFromStructure(structure sexp) []mail.Attachment {
	found := []mail.Attachment{}
	var walk func(sexp)
	walk = func(node sexp) {
		if !node.isList || len(node.list) == 0 {
			return
		}
		if node.list[0].isList {
			for _, child := range node.list {
				walk(child)
			}
			return
		}
		if len(node.list) < 2 || node.list[1].isList {
			return
		}
		kind := strings.ToLower(node.list[0].atom + "/" + node.list[1].atom)
		var flat []string
		for _, child := range node.list[2:] {
			flat = flatten(child, flat)
		}
		name := ""
		disposition := false
		for i, item := range flat {
			low := strings.ToLower(item)
			if (low == "filename" || low == "name") && i+1 < len(flat) {
				name = flat[i+1]
			}
			if low == "attachment" {
				disposition = true
			}
		}
		if kind == "message/rfc822" {
			if disposition {
				found = append(found, mail.Attachment{Kind: kind, Name: name})
			}
		} else if name != "" || disposition {
			found = append(found, mail.Attachment{Kind: kind, Name: name})
		}
	}
	walk(structure)
	return found
}

func structureAttachments(meta string) ([]mail.Attachment, error) {
	m, err := structureRE.Search(meta)
	if err != nil || m == nil {
		return nil, err
	}
	text := []rune(meta)
	if m.End() >= len(text) || text[m.End()] != '(' {
		return nil, nil
	}
	block, ok := balanced(text, m.End())
	if !ok {
		return nil, nil
	}
	structure, err := parseSexp(string(block))
	if errors.Is(err, errSexp) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return attachmentsFromStructure(structure), nil
}

type fetchGroup struct {
	meta         string
	header, text []byte
}

func parseFetch(data []datum) ([]mail.Message, error) {
	var groups []*fetchGroup
	var current *fetchGroup
	for _, item := range data {
		switch {
		case item.tuple:
			prefix := latin1(item.text)
			start, err := groupStartRE.Match(prefix)
			if err != nil {
				return nil, err
			}
			if start != nil {
				current = &fetchGroup{meta: prefix, header: []byte{}, text: []byte{}}
				groups = append(groups, current)
			} else if current != nil {
				current.meta += prefix
			}
			if current == nil {
				continue
			}
			m, err := sectionRE.Search(prefix)
			if err != nil {
				return nil, err
			}
			if m != nil {
				if m.Group(1) == "HEADER" {
					current.header = item.lit
				} else {
					current.text = item.lit
				}
			}
		case !item.none:
			line := latin1(item.text)
			start, err := groupStartRE.Match(line)
			if err != nil {
				return nil, err
			}
			if start != nil {
				current = &fetchGroup{meta: line, header: []byte{}, text: []byte{}}
				groups = append(groups, current)
			} else if current != nil {
				current.meta += " " + line
			}
		}
	}
	out := []mail.Message{}
	for _, g := range groups {
		m, err := uidRE.Search(g.meta)
		if err != nil {
			return nil, err
		}
		if m == nil {
			continue
		}
		msg, err := fetched(g)
		if err != nil {
			return nil, err
		}
		out = append(out, msg)
	}
	return out, nil
}

func group1(re *rx.Regexp, text string) (string, bool, error) {
	m, err := re.Search(text)
	if err != nil || m == nil {
		return "", false, err
	}
	return m.Group(1), true, nil
}

func fetched(g *fetchGroup) (mail.Message, error) {
	meta := g.meta
	msg := mail.Message{Header: g.header, Text: g.text}
	uid, _, err := group1(uidRE, meta)
	if err != nil {
		return msg, err
	}
	if msg.UID, err = pyInt32(uid); err != nil {
		return msg, err
	}
	flags, _, err := section(meta, flagsRE)
	if err != nil {
		return msg, err
	}
	msg.Flags = pySplit(flags)
	labels, ok, err := section(meta, labelsRE)
	if err != nil {
		return msg, err
	}
	if ok {
		if msg.Labels, err = parseLabels(labels); err != nil {
			return msg, err
		}
	}
	if v, ok, err := group1(modseqRE, meta); err != nil {
		return msg, err
	} else if ok {
		if msg.ModSeq, err = pyInt64(v); err != nil {
			return msg, err
		}
	}
	if v, ok, err := group1(sizeRE, meta); err != nil {
		return msg, err
	} else if ok {
		size, err := pyInt64(v)
		if err != nil {
			return msg, err
		}
		msg.Size = int64(size)
	}
	if msg.GmMsgID, _, err = group1(gmMsgIDRE, meta); err != nil {
		return msg, err
	}
	if msg.GmThrID, _, err = group1(gmThrIDRE, meta); err != nil {
		return msg, err
	}
	if msg.InternalDate, _, err = group1(internalRE, meta); err != nil {
		return msg, err
	}
	msg.Attachments, err = structureAttachments(meta)
	return msg, err
}

func parseStatus(data []datum) (mail.Status, error) {
	text := joinData(data, false)
	var st mail.Status
	for _, f := range []struct {
		re  *rx.Regexp
		set func(string) error
	}{
		{uidValidityRE, func(v string) (err error) { st.UIDValidity, err = pyInt32(v); return }},
		{uidNextRE, func(v string) (err error) { st.UIDNext, err = pyInt32(v); return }},
		{highestRE, func(v string) error {
			n, err := pyInt64(v)
			st.HighestModSeq = &n
			return err
		}},
		{messagesRE, func(v string) (err error) { st.Messages, err = pyInt32(v); return }},
	} {
		v, ok, err := group1(f.re, text)
		if err != nil {
			return st, err
		}
		if ok {
			if err := f.set(v); err != nil {
				return st, err
			}
		}
	}
	return st, nil
}

func parseSearch(data []datum) ([]uint32, error) {
	first := data[0]
	if first.tuple {
		return nil, errors.New("'tuple' object has no attribute 'split'")
	}
	out := []uint32{}
	for _, f := range strings.FieldsFunc(string(first.text), func(r rune) bool {
		return r == ' ' || (r >= '\t' && r <= '\r')
	}) {
		u, err := pyInt32(f)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, nil
}

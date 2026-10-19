package mime

import (
	"errors"
	"slices"
)

var errHeader = errors.New("header value cannot be parsed")

type field struct {
	name   []rune
	key    string
	value  []rune
	source []rune
}

func unfold(v []rune) []rune {
	out := make([]rune, 0, len(v))
	for _, r := range v {
		if r != '\r' && r != '\n' {
			out = append(out, r)
		}
	}
	return out
}

func headerKind(name []rune) string {
	switch string(lowerRunes(name)) {
	case "date", "resent-date", "orig-date":
		return "date"
	case "sender", "resent-sender", "to", "resent-to", "cc", "resent-cc", "bcc", "resent-bcc", "from", "resent-from", "reply-to":
		return "address"
	case "mime-version":
		return "mime-version"
	case "content-type":
		return "content-type"
	case "content-disposition":
		return "content-disposition"
	case "content-transfer-encoding":
		return "cte"
	case "message-id":
		return "message-id"
	case "in-reply-to", "references":
		return "references"
	}
	return "unstructured"
}

func headerValue(name, raw []rune, level int, all bool) (s string, err error) {
	defer func() {
		if r := recover(); r != nil {
			s, err = "", errHeader
			if _, ok := r.(fatal); !ok {
				err = unexpected(r)
			}
		}
	}()
	v := unfold(raw)
	var tree *token
	kind := headerKind(name)
	switch kind {
	case "date":
		return dateHeader(v), nil
	case "address":
		return addressHeader(v, commentLimit(kind, level, all))
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
	if err != nil || maxComment(tree) > commentLimit(kind, level, all) {
		return "", errHeader
	}
	return sanitize(tree.str()), nil
}

func dateHeader(v []rune) string {
	if len(v) == 0 {
		return ""
	}
	if d, ok := parseDate(v); ok {
		return d.format()
	}
	return sanitize(v)
}

type pyAddress struct {
	display, username, domain []rune
}

type pyGroup struct {
	display   []rune
	hasName   bool
	addresses []pyAddress
}

func addressHeader(v []rune, limit int) (string, error) {
	al, err := getAddressList(v)
	if err != nil || maxComment(al) > limit {
		return "", errHeader
	}
	var out []rune
	n := 0
	for _, a := range al.kids {
		if a.typ != "address" {
			continue
		}
		g := pyGroup{}
		g.display, g.hasName = a.displayName()
		for _, mb := range a.allMailboxes() {
			var addr pyAddress
			if mb.typ == "mailbox" || mb.mailbox {
				addr.display, _ = mb.displayName()
				addr.username, _ = mb.localPart()
				addr.domain, _ = mb.domain()
			}
			for _, part := range [][]rune{addr.display, addr.username, addr.domain} {
				if indexRune(part, '\r') >= 0 || indexRune(part, '\n') >= 0 {
					return "", errHeader
				}
			}
			g.addresses = append(g.addresses, addr)
		}
		if n > 0 {
			out = append(out, ',', ' ')
		}
		n++
		out = append(out, g.str()...)
	}
	return sanitize(out), nil
}

func disjoint(set string, s []rune) bool {
	for _, r := range s {
		if inSet(set, r) {
			return false
		}
	}
	return true
}

func (a pyAddress) addrSpec() []rune {
	lp := a.username
	if !disjoint(dotAtomEnds, lp) {
		lp = quoteString(lp)
	}
	if len(a.domain) > 0 {
		return append(append(slices.Clone(lp), '@'), a.domain...)
	}
	if len(lp) == 0 {
		return []rune("<>")
	}
	return lp
}

func (a pyAddress) str() []rune {
	disp := a.display
	if !disjoint(specials, disp) {
		disp = quoteString(disp)
	}
	if len(disp) > 0 {
		spec := a.addrSpec()
		if string(spec) == "<>" {
			spec = nil
		}
		out := append(slices.Clone(disp), ' ', '<')
		out = append(out, spec...)
		return append(out, '>')
	}
	return a.addrSpec()
}

func (g pyGroup) str() []rune {
	if !g.hasName && len(g.addresses) == 1 {
		return g.addresses[0].str()
	}
	disp := []rune("None")
	if g.hasName {
		disp = g.display
		if !disjoint(specials, disp) {
			disp = quoteString(disp)
		}
	}
	var addrs []rune
	for i, a := range g.addresses {
		if i > 0 {
			addrs = append(addrs, ',', ' ')
		}
		addrs = append(addrs, a.str()...)
	}
	out := append(slices.Clone(disp), ':')
	if len(addrs) > 0 {
		out = append(append(out, ' '), addrs...)
	}
	return append(out, ';')
}

func contentDisposition(raw []rune, level int) (string, bool, error) {
	if _, err := headerValue([]rune("content-disposition"), raw, level, false); err != nil {
		return "", false, err
	}
	tree, err := parseContentDisposition(unfold(raw))
	if err != nil {
		return "", false, errHeader
	}
	if !tree.hasDisposition {
		return "", false, nil
	}
	return sanitize(tree.disposition), true, nil
}

// Package mime reproduces the parts of CPython 3.14's email package (email.message_from_bytes with
// policy.default, its header registry and parsers, email.utils.parseaddr/getaddresses and
// parsedate_to_datetime) that the frozen Python app relies on, so ported code sees the same strings,
// structure and failures that Python produced for the same bytes.
package mime

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"strings"
)

// MaxDepth is the deepest message nesting (multipart and message/* containers) CPython 3.14.7 parses before
// RecursionError, measured with features.extract called four frames below a module; Parse fails deeper, as
// Python raises.
const MaxDepth = 970

var errTooDeep = errors.New("message nesting exceeds Python's recursion limit")

// Message is one parsed message or MIME part.
type Message struct {
	fields      []field
	payload     []byte
	parts       []*Message
	multipart   bool
	defaultType string
	level       int
}

type lineReader struct {
	lines [][]byte
	pos   int
	back  [][]byte
	eofs  []func([]byte) bool
}

func splitLines(b []byte) [][]byte {
	var out [][]byte
	for len(b) > 0 {
		i := bytes.IndexAny(b, "\r\n")
		if i < 0 {
			out = append(out, b)
			break
		}
		end := i + 1
		if b[i] == '\r' && end < len(b) && b[end] == '\n' {
			end++
		}
		out = append(out, b[:end])
		b = b[end:]
	}
	return out
}

func (r *lineReader) readline() ([]byte, bool) {
	var line []byte
	switch {
	case len(r.back) > 0:
		line = r.back[len(r.back)-1]
	case r.pos < len(r.lines):
		line = r.lines[r.pos]
	default:
		return nil, false
	}
	for i := len(r.eofs) - 1; i >= 0; i-- {
		if r.eofs[i](line) {
			return nil, false
		}
	}
	if len(r.back) > 0 {
		r.back = r.back[:len(r.back)-1]
	} else {
		r.pos++
	}
	return line, true
}

func (r *lineReader) unread(line []byte) { r.back = append(r.back, line) }

type boundaryKind int

const (
	boundaryNone boundaryKind = iota
	boundaryPart
	boundaryEnd
)

func boundaryMatch(line, sep []byte) boundaryKind {
	if sep == nil || !bytes.HasPrefix(line, sep) {
		return boundaryNone
	}
	rest := line[len(sep):]
	kind := boundaryPart
	if bytes.HasPrefix(rest, []byte("--")) && validBoundaryTail(rest[2:]) {
		return boundaryEnd
	}
	if !validBoundaryTail(rest) {
		return boundaryNone
	}
	return kind
}

func validBoundaryTail(rest []byte) bool {
	rest = bytes.TrimLeft(rest, " \t")
	switch string(rest) {
	case "", "\n", "\r", "\r\n":
		return true
	}
	return false
}

func isHeaderLine(line []byte) bool {
	if bytes.HasPrefix(line, []byte("From ")) || len(line) > 0 && (line[0] == ' ' || line[0] == '\t') {
		return true
	}
	for _, c := range line {
		switch {
		case c == ':':
			return true
		case c < '!' || c > '~':
			return false
		}
	}
	return false
}

func isNewline(line []byte) bool { return len(line) > 0 && (line[0] == '\r' || line[0] == '\n') }

func trimEOL(b []byte) []byte {
	switch {
	case bytes.HasSuffix(b, []byte("\r\n")):
		return b[:len(b)-2]
	case bytes.HasSuffix(b, []byte("\n")), bytes.HasSuffix(b, []byte("\r")):
		return b[:len(b)-1]
	}
	return b
}

func unexpected(r any) error {
	slog.Error("mime: recovered from a parser panic", "ERR", fmt.Sprint(r))
	return fmt.Errorf("mime: internal error: %v", r)
}

func guard(err *error) {
	if r := recover(); r != nil {
		if _, ok := r.(fatal); ok {
			*err = errHeader
		} else {
			*err = unexpected(r)
		}
	}
}

// Parse is email.message_from_bytes(data, policy=email.policy.default). err is set where Python raises: a
// Content-Type or multipart Content-Transfer-Encoding value whose parse raises.
func Parse(data []byte) (msg *Message, err error) {
	defer guard(&err)
	r := &lineReader{lines: splitLines(data)}
	return parseMessage(r, "text/plain", 0)
}

func parseMessage(r *lineReader, defaultType string, depth int) (*Message, error) {
	m := &Message{defaultType: defaultType, level: depth}
	var headers [][]byte
	for {
		line, ok := r.readline()
		if !ok {
			break
		}
		if !isHeaderLine(line) {
			if !isNewline(line) {
				r.unread(line)
			}
			break
		}
		headers = append(headers, line)
	}
	m.parseHeaders(headers, r)
	if depth > MaxDepth {
		return nil, errTooDeep
	}
	ctype, err := m.contentType()
	if err != nil {
		return nil, err
	}
	switch {
	case ctype == "message/delivery-status":
		return m, m.parseDeliveryStatus(r, depth)
	case strings.HasPrefix(ctype, "message/"):
		sub, err := parseMessage(r, "text/plain", depth+1)
		if err != nil {
			return nil, err
		}
		m.multipart, m.parts = true, []*Message{sub}
		return m, nil
	case strings.HasPrefix(ctype, "multipart/"):
		return m, m.parseMultipart(r, ctype, depth)
	}
	m.payload = readRest(r)
	return m, nil
}

func (m *Message) parseDeliveryStatus(r *lineReader, depth int) error {
	for {
		r.eofs = append(r.eofs, isNewline)
		sub, err := parseMessage(r, "text/plain", depth+1)
		r.eofs = r.eofs[:len(r.eofs)-1]
		if err != nil {
			return err
		}
		m.multipart, m.parts = true, append(m.parts, sub)
		r.readline()
		line, ok := r.readline()
		if !ok {
			return nil
		}
		r.unread(line)
	}
}

func readRest(r *lineReader) []byte {
	var out []byte
	for {
		line, ok := r.readline()
		if !ok {
			return out
		}
		out = append(out, line...)
	}
}

func (m *Message) parseHeaders(lines [][]byte, r *lineReader) {
	var last [][]byte
	flush := func() {
		if last == nil {
			return
		}
		joined := escapeBytes(bytes.Join(last, nil))
		i := indexRune(joined, ':')
		value := rstripNewlines(lstripSet(joined[i+1:], " \t\r\n"))
		m.fields = append(m.fields, field{name: joined[:i], key: string(lowerRunes(joined[:i])), value: value})
		last = nil
	}
	for n, line := range lines {
		if line[0] == ' ' || line[0] == '\t' {
			if last != nil {
				last = append(last, line)
			}
			continue
		}
		flush()
		if bytes.HasPrefix(line, []byte("From ")) {
			if n == len(lines)-1 && n != 0 {
				r.unread(line)
				return
			}
			continue
		}
		if line[0] == ':' {
			continue
		}
		last = [][]byte{line}
	}
	flush()
}

func lstripSet(v []rune, set string) []rune {
	for len(v) > 0 && inSet(set, v[0]) {
		v = v[1:]
	}
	return v
}

func rstripNewlines(v []rune) []rune {
	for len(v) > 0 && (v[len(v)-1] == '\r' || v[len(v)-1] == '\n') {
		v = v[:len(v)-1]
	}
	return v
}

func (m *Message) parseMultipart(r *lineReader, ctype string, depth int) error {
	bv, ok, err := m.rawParam("boundary", "content-type")
	if err != nil {
		return err
	}
	if !ok {
		m.payload = readRest(r)
		return nil
	}
	if _, _, err := m.Get("content-transfer-encoding"); err != nil {
		return err
	}
	boundary := string(rstrip([]rune(bv.collapse())))
	var sep []byte
	if isASCII(boundary) {
		sep = []byte("--" + boundary)
	}
	matchSep := func(line []byte) boundaryKind {
		if sep == nil {
			return boundaryNone
		}
		return boundaryMatch(line, sep)
	}
	childType := "text/plain"
	if ctype == "multipart/digest" {
		childType = "message/rfc822"
	}
	var preamble []byte
	capturing := true
	for {
		line, ok := r.readline()
		if !ok {
			break
		}
		kind := matchSep(line)
		if kind == boundaryEnd {
			break
		}
		if kind == boundaryNone {
			preamble = append(preamble, line...)
			continue
		}
		if capturing {
			capturing = false
			r.unread(line)
			continue
		}
		for {
			next, ok := r.readline()
			if !ok {
				break
			}
			if matchSep(next) == boundaryNone {
				r.unread(next)
				break
			}
		}
		r.eofs = append(r.eofs, func(line []byte) bool { return matchSep(line) != boundaryNone })
		part, err := parseMessage(r, childType, depth+1)
		r.eofs = r.eofs[:len(r.eofs)-1]
		if err != nil {
			return err
		}
		if !part.multipart && !strings.HasPrefix(part.ContentType(), "multipart/") {
			part.payload = trimEOL(part.payload)
		}
		m.parts = append(m.parts, part)
		m.multipart = true
	}
	if capturing {
		m.payload = preamble
	}
	readRest(r)
	return nil
}

func isASCII(s string) bool {
	for i := range len(s) {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

var errNoHeader = errors.New("no such header")

// Keys is Message.keys(): every header name in order, duplicates included.
func (m *Message) Keys() []string {
	out := make([]string, len(m.fields))
	for i, f := range m.fields {
		out[i] = sanitizeName(f.name)
	}
	return out
}

func sanitizeName(name []rune) string { return string(unescapeBytes(name)) }

func (m *Message) find(name string) (field, bool) {
	want := Lower(name)
	for _, f := range m.fields {
		if f.key == want {
			return f, true
		}
	}
	return field{}, false
}

// Has is Python's `name in msg`.
func (m *Message) Has(name string) bool {
	_, ok := m.find(name)
	return ok
}

// Get is str(msg.get(name)): the first header named name as its header class renders it. ok is false when the
// header is absent; err is set where Python raises while parsing the value.
func (m *Message) Get(name string) (string, bool, error) {
	f, ok := m.find(name)
	if !ok {
		return "", false, nil
	}
	s, err := headerValue(f.name, f.value, m.level, false)
	return s, true, err
}

// GetAll is msg.get_all(name) rendered with str; err is set where Python raises for any of the values.
func (m *Message) GetAll(name string) ([]string, error) {
	want := Lower(name)
	var out []string
	for _, f := range m.fields {
		if f.key != want {
			continue
		}
		s, err := headerValue(f.name, f.value, m.level, true)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

// ContentType is get_content_type(); a Content-Type value Python cannot parse yields its default type, as
// Python's caller sees the exception.
func (m *Message) ContentType() string {
	ct, _ := m.contentType()
	return ct
}

func (m *Message) contentType() (string, error) {
	v, ok, err := m.Get("content-type")
	if err != nil {
		return "", err
	}
	if !ok {
		return m.defaultType, nil
	}
	ctype, _, _ := strings.Cut(v, ";")
	ctype = Lower(Strip(ctype))
	if strings.Count(ctype, "/") != 1 {
		return "text/plain", nil
	}
	return ctype, nil
}

// Strip is Python's str.strip().
func Strip(s string) string { return string(strip([]rune(s))) }

// IsMultipart is is_multipart(): the payload is a list of parts.
func (m *Message) IsMultipart() bool { return m.multipart }

// Parts is iter_parts().
func (m *Message) Parts() []*Message { return m.parts }

// Param is get_param(name, header=header) with values unquoted; RFC 2231 values come back collapsed into the
// tuple form's text.
func (m *Message) Param(name, header string) (string, bool, error) {
	v, ok, err := m.rawParam(name, header)
	if err != nil || !ok {
		return "", ok, err
	}
	if v.tuple {
		return v.text, true, errTupleParam
	}
	return v.text, true, nil
}

var errTupleParam = errors.New("RFC 2231 parameter")

func (m *Message) rawParam(name, header string) (paramValue, bool, error) {
	if !m.Has(header) {
		return paramValue{}, false, nil
	}
	v, _, err := m.Get(header)
	if err != nil {
		return paramValue{}, false, err
	}
	for _, p := range decodeParams(parseParams(v)) {
		if Lower(p.name) == Lower(name) {
			if p.value.tuple {
				p.value.text = unquote(p.value.text)
			} else {
				p.value.text = unquote(p.value.text)
			}
			return p.value, true, nil
		}
	}
	return paramValue{}, false, nil
}

// Filename is get_filename(): Content-Disposition filename, else Content-Type name.
func (m *Message) Filename() (name string, found bool, err error) {
	defer guard(&err)
	v, found, err := m.rawParam("filename", "content-disposition")
	if err != nil {
		return "", false, err
	}
	if !found {
		v, found, err = m.rawParam("name", "content-type")
		if err != nil || !found {
			return "", false, err
		}
	}
	return Strip(v.collapse()), true, nil
}

// IsAttachment is is_attachment().
func (m *Message) IsAttachment() (bool, error) {
	f, ok := m.find("content-disposition")
	if !ok {
		return false, nil
	}
	d, has, err := contentDisposition(f.value, m.level)
	if err != nil {
		return false, err
	}
	return has && d == "attachment", nil
}

// Body is get_body(preferencelist=prefs).
func (m *Message) Body(prefs ...string) (*Message, error) {
	best := len(prefs)
	var body *Message
	err := m.findBody(m, prefs, func(prio int, part *Message) bool {
		if prio < best {
			best, body = prio, part
			return prio == 0
		}
		return false
	})
	if errors.Is(err, errStop) {
		err = nil
	}
	return body, err
}

func (m *Message) findBody(part *Message, prefs []string, yield func(int, *Message) bool) error {
	att, err := part.IsAttachment()
	if err != nil || att {
		return err
	}
	ctype, err := part.contentType()
	if err != nil {
		return err
	}
	maintype, subtype, _ := strings.Cut(ctype, "/")
	if maintype == "text" {
		for i, p := range prefs {
			if p == subtype {
				if yield(i, part) {
					return errStop
				}
				break
			}
		}
		return nil
	}
	if maintype != "multipart" || !m.multipart {
		return nil
	}
	if subtype != "related" {
		for _, sub := range part.parts {
			if err := m.findBody(sub, prefs, yield); err != nil {
				return err
			}
		}
		return nil
	}
	for i, p := range prefs {
		if p == "related" {
			if yield(i, part) {
				return errStop
			}
			break
		}
	}
	var candidate *Message
	start, ok, err := part.Param("start", "content-type")
	if err != nil && !errors.Is(err, errTupleParam) {
		return err
	}
	if ok && start != "" && err == nil {
		for _, sub := range part.parts {
			id, has, err := sub.Get("content-id")
			if err != nil {
				return err
			}
			if has && id == start {
				candidate = sub
				break
			}
		}
	}
	if candidate == nil {
		switch {
		case part.multipart && len(part.parts) > 0:
			candidate = part.parts[0]
		case !part.multipart && len(part.payload) > 0:
			return errNotMessage
		}
	}
	if candidate != nil {
		return m.findBody(candidate, prefs, yield)
	}
	return nil
}

var errStop = errors.New("stop")

var errNotMessage = errors.New("payload is text, not a list of parts")

// Attachments is iter_attachments(); it returns the parts yielded before any exception Python would raise, and
// that exception.
func (m *Message) Attachments() ([]*Message, error) {
	ctype, err := m.contentType()
	if err != nil {
		return nil, err
	}
	maintype, subtype, _ := strings.Cut(ctype, "/")
	if maintype != "multipart" || subtype == "alternative" || !m.multipart {
		return nil, nil
	}
	parts := m.parts
	if subtype == "related" {
		start, ok, err := m.Param("start", "content-type")
		if err != nil && !errors.Is(err, errTupleParam) {
			return nil, err
		}
		if ok && start != "" && err == nil {
			var out []*Message
			found := false
			for _, p := range parts {
				id, has, err := p.Get("content-id")
				if err != nil {
					return nil, err
				}
				if has && id == start {
					found = true
				} else {
					out = append(out, p)
				}
			}
			if found {
				return out, nil
			}
		}
		if len(parts) == 0 {
			return nil, errNoHeader
		}
		return parts[1:], nil
	}
	var out []*Message
	var seen []string
	for _, p := range parts {
		ct, err := p.contentType()
		if err != nil {
			return out, err
		}
		pm, ps, _ := strings.Cut(ct, "/")
		body := pm == "text" && (ps == "plain" || ps == "html") || pm == "multipart" && (ps == "related" || ps == "alternative")
		if body {
			att, err := p.IsAttachment()
			if err != nil {
				return out, err
			}
			if !att && !containsString(seen, ps) {
				seen = append(seen, ps)
				continue
			}
		}
		out = append(out, p)
	}
	return out, nil
}

func containsString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// Payload is get_payload(decode=True) for a non-multipart message.
func (m *Message) Payload() (out []byte, err error) {
	defer guard(&err)
	f, ok := m.find("content-transfer-encoding")
	cte := ""
	if ok {
		if _, err := headerValue(f.name, f.value, m.level, false); err != nil {
			return nil, err
		}
		tree, err := parseCTE(unfold(f.value))
		if err != nil {
			return nil, err
		}
		cte = "7bit"
		if len(tree.kids) > 0 && tree.kids[0].typ == "token" {
			cte = sanitize(lowerRunes(strip(tree.kids[0].value())))
		}
	}
	switch cte {
	case "quoted-printable":
		return a2bQP(m.payload, false), nil
	case "base64":
		return decodeBase64Payload(m.payload), nil
	case "x-uuencode", "uuencode", "uue", "x-uue":
		if out, err := decodeUU(m.payload); err == nil {
			return out, nil
		}
	}
	return m.payload, nil
}

// Content is get_content() for a text part: the decoded payload in its charset parameter (default ASCII) with
// errors replaced. err is set where Python raises (unknown charset, unparseable header, non-text part).
func (m *Message) Content() (s string, err error) {
	defer guard(&err)
	ctype, err := m.contentType()
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(ctype, "text/") {
		return "", errNotText
	}
	if m.multipart {
		return "", errNotText
	}
	payload, err := m.Payload()
	if err != nil {
		return "", err
	}
	charset, ok, err := m.Param("charset", "content-type")
	if err != nil {
		return "", err
	}
	if !ok {
		charset = "ASCII"
	}
	if hasSurrogates([]rune(charset)) || strings.ContainsRune(charset, 0) {
		return "", errLookup
	}
	out, err := decodeBytes(payload, charset, errReplace)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

var errNotText = errors.New("not a text part")

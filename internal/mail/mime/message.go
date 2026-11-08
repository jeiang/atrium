package mime

import (
	"bytes"
	"encoding/base64"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"net/textproto"
	"strings"

	"github.com/jeiang/atrium/internal/textutil"
)

type Message struct {
	header    mail.Header
	body      []byte
	parts     []*Message
	multipart bool
	mediaType string
	params    map[string]string
}

func Parse(data []byte) *Message {
	head, body := splitHeader(data)
	m := newMessage(parseHeaderBlock(head))
	m.load(body)
	return m
}

func splitHeader(data []byte) (head, body []byte) {
	pos := 0
	for pos < len(data) {
		line, next := nextLine(data, pos)
		if len(line) == 0 {
			return data[:pos], data[next:]
		}
		pos = next
	}
	return data, nil
}

func nextLine(data []byte, pos int) (line []byte, next int) {
	i := bytes.IndexAny(data[pos:], "\r\n")
	if i < 0 {
		return data[pos:], len(data)
	}
	end := pos + i
	next = end + 1
	if data[end] == '\r' && next < len(data) && data[next] == '\n' {
		next++
	}
	return data[pos:end], next
}

func newMessage(header mail.Header) *Message {
	mediaType, params := parseContentType(header.Get("Content-Type"))
	return &Message{header: header, mediaType: mediaType, params: params}
}

func (m *Message) load(body []byte) {
	switch {
	case m.mediaType == "message/rfc822" || m.mediaType == "message/global":
		m.parts = []*Message{Parse(body)}
		m.multipart = true
	case strings.HasPrefix(m.mediaType, "multipart/"):
		m.loadMultipart(body)
	default:
		m.body = body
	}
}

func (m *Message) loadMultipart(body []byte) {
	boundary := m.params["boundary"]
	if boundary == "" {
		m.body = body
		return
	}
	r := multipart.NewReader(bytes.NewReader(body), boundary)
	for {
		part, err := r.NextRawPart()
		if err != nil {
			break
		}
		data, err := io.ReadAll(part)
		sub := newMessage(mail.Header(part.Header))
		sub.load(data)
		m.parts = append(m.parts, sub)
		if err != nil {
			break
		}
	}
	m.multipart = len(m.parts) > 0
}

func (m *Message) Header(name string) string {
	return decodeWords(m.header.Get(name))
}

func (m *Message) Headers(name string) []string {
	values := textproto.MIMEHeader(m.header).Values(name)
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = decodeWords(v)
	}
	return out
}

func (m *Message) Keys() []string {
	out := make([]string, 0, len(m.header))
	for key := range m.header {
		out = append(out, key)
	}
	return out
}

func (m *Message) Addresses(name string) []mail.Address {
	var out []mail.Address
	for _, value := range textproto.MIMEHeader(m.header).Values(name) {
		out = append(out, ParseAddresses(value)...)
	}
	return out
}

func (m *Message) ContentType() string { return m.mediaType }

func (m *Message) Param(name string) string { return m.params[strings.ToLower(name)] }

func (m *Message) IsMultipart() bool { return m.multipart }

func (m *Message) Parts() []*Message { return m.parts }

func (m *Message) IsAttachment() bool {
	value := m.header.Get("Content-Disposition")
	if value == "" {
		return false
	}
	mediaType, _, err := mime.ParseMediaType(value)
	if err != nil {
		mediaType, _ = lenientMediaType(value)
	}
	return strings.EqualFold(strings.TrimSpace(mediaType), "attachment")
}

func (m *Message) Filename() string {
	if value := m.header.Get("Content-Disposition"); value != "" {
		if _, params, err := mime.ParseMediaType(value); err == nil {
			if name := params["filename"]; name != "" {
				return decodeWords(name)
			}
		} else if _, params := lenientMediaType(value); params["filename"] != "" {
			return decodeWords(params["filename"])
		}
	}
	if name := m.params["name"]; name != "" {
		return decodeWords(name)
	}
	return ""
}

func (m *Message) Body(prefs ...string) *Message {
	best := len(prefs)
	var found *Message
	m.walkBody(prefs, func(prio int, part *Message) bool {
		if prio < best {
			best, found = prio, part
			return prio == 0
		}
		return false
	})
	return found
}

func (m *Message) walkBody(prefs []string, yield func(int, *Message) bool) bool {
	if m.IsAttachment() {
		return false
	}
	maintype, subtype, _ := strings.Cut(m.mediaType, "/")
	if maintype == "text" {
		for i, p := range prefs {
			if p == subtype {
				return yield(i, m)
			}
		}
		return false
	}
	if maintype != "multipart" || !m.multipart {
		return false
	}
	if subtype != "related" {
		for _, part := range m.parts {
			if part.walkBody(prefs, yield) {
				return true
			}
		}
		return false
	}
	for i, p := range prefs {
		if p == "related" {
			if yield(i, m) {
				return true
			}
			break
		}
	}
	candidate := m.startPart()
	if candidate == nil && len(m.parts) > 0 {
		candidate = m.parts[0]
	}
	if candidate == nil {
		return false
	}
	return candidate.walkBody(prefs, yield)
}

func (m *Message) startPart() *Message {
	start := m.params["start"]
	if start == "" {
		return nil
	}
	for _, part := range m.parts {
		if strings.TrimSpace(part.header.Get("Content-ID")) == start {
			return part
		}
	}
	return nil
}

func (m *Message) Attachments() []*Message {
	if !m.multipart {
		return nil
	}
	_, subtype, _ := strings.Cut(m.mediaType, "/")
	if subtype == "alternative" {
		return nil
	}
	if subtype == "related" {
		start := m.startPart()
		if start != nil {
			out := make([]*Message, 0, len(m.parts)-1)
			for _, part := range m.parts {
				if part != start {
					out = append(out, part)
				}
			}
			return out
		}
		if len(m.parts) == 0 {
			return nil
		}
		return m.parts[1:]
	}
	var out []*Message
	seen := map[string]bool{}
	for _, part := range m.parts {
		maintype, sub, _ := strings.Cut(part.mediaType, "/")
		body := maintype == "text" && (sub == "plain" || sub == "html") ||
			maintype == "multipart" && (sub == "related" || sub == "alternative")
		if body && !part.IsAttachment() && !seen[sub] {
			seen[sub] = true
			continue
		}
		out = append(out, part)
	}
	return out
}

func (m *Message) Payload() []byte {
	switch strings.ToLower(strings.TrimSpace(m.header.Get("Content-Transfer-Encoding"))) {
	case "quoted-printable":
		return decodeQuotedPrintable(m.body)
	case "base64":
		return decodeBase64(m.body)
	}
	return m.body
}

func (m *Message) Content() string {
	if m.multipart || !strings.HasPrefix(m.mediaType, "text/") {
		return ""
	}
	return textutil.Decode(m.Payload(), m.params["charset"])
}

func decodeQuotedPrintable(data []byte) []byte {
	out, err := io.ReadAll(quotedprintable.NewReader(bytes.NewReader(data)))
	if err == nil {
		return out
	}
	return lenientQuotedPrintable(data)
}

func lenientQuotedPrintable(data []byte) []byte {
	out := make([]byte, 0, len(data))
	for i := 0; i < len(data); i++ {
		if data[i] != '=' {
			out = append(out, data[i])
			continue
		}
		switch {
		case i+1 < len(data) && data[i+1] == '\n':
			i++
		case i+2 < len(data) && data[i+1] == '\r' && data[i+2] == '\n':
			i += 2
		case i+2 < len(data) && isHex(data[i+1]) && isHex(data[i+2]):
			out = append(out, hexValue(data[i+1])<<4|hexValue(data[i+2]))
			i += 2
		default:
			out = append(out, '=')
		}
	}
	return out
}

func isHex(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

func hexValue(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	}
	return c - 'A' + 10
}

func decodeBase64(data []byte) []byte {
	clean := make([]byte, 0, len(data))
	for _, c := range data {
		if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '+' || c == '/' || c == '=' {
			clean = append(clean, c)
		}
	}
	if out, err := base64.StdEncoding.DecodeString(string(clean)); err == nil {
		return out
	}
	if out, err := base64.RawStdEncoding.DecodeString(strings.TrimRight(string(clean), "=")); err == nil {
		return out
	}
	return data
}

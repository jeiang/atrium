package mime

import (
	"bytes"
	"io"
	"mime"
	"net/mail"
	"net/textproto"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/textutil"
)

var wordDecoder = mime.WordDecoder{CharsetReader: charsetReader}

var addressParser = mail.AddressParser{WordDecoder: &wordDecoder}

func charsetReader(label string, input io.Reader) (io.Reader, error) {
	data, err := io.ReadAll(input)
	if err != nil {
		return nil, err
	}
	return strings.NewReader(textutil.Decode(data, label)), nil
}

func decodeWords(value string) string {
	if !strings.Contains(value, "=?") {
		return value
	}
	out, err := wordDecoder.DecodeHeader(value)
	if err != nil {
		return value
	}
	return out
}

func parseHeaderBlock(data []byte) mail.Header {
	header := mail.Header{}
	var name, value string
	flush := func() {
		if name == "" {
			return
		}
		key := textproto.CanonicalMIMEHeaderKey(name)
		header[key] = append(header[key], strings.TrimSpace(value))
		name, value = "", ""
	}
	for len(data) > 0 {
		line, next := nextLine(data, 0)
		data = data[next:]
		if len(line) > 0 && (line[0] == ' ' || line[0] == '\t') {
			if name != "" {
				value += " " + strings.TrimSpace(string(line))
			}
			continue
		}
		flush()
		if len(line) == 0 || bytes.HasPrefix(line, []byte("From ")) {
			continue
		}
		colon := bytes.IndexByte(line, ':')
		if colon <= 0 {
			continue
		}
		name = strings.TrimSpace(string(line[:colon]))
		value = strings.TrimSpace(string(line[colon+1:]))
	}
	flush()
	return header
}

func RawHeader(data []byte, name string) string {
	head, _ := splitHeader(data)
	return parseHeaderBlock(head).Get(name)
}

func parseContentType(value string) (string, map[string]string) {
	if strings.TrimSpace(value) == "" {
		return "text/plain", nil
	}
	mediaType, params, err := mime.ParseMediaType(value)
	if err != nil {
		mediaType, params = lenientMediaType(value)
	}
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	if strings.Count(mediaType, "/") != 1 {
		return "text/plain", params
	}
	return mediaType, params
}

func lenientMediaType(value string) (string, map[string]string) {
	head, rest, _ := strings.Cut(value, ";")
	params := map[string]string{}
	for _, param := range strings.Split(rest, ";") {
		key, val, ok := strings.Cut(param, "=")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		val = strings.TrimSpace(val)
		if len(val) >= 2 && val[0] == '"' && val[len(val)-1] == '"' {
			val = val[1 : len(val)-1]
		}
		if key != "" {
			params[key] = val
		}
	}
	return strings.TrimSpace(head), params
}

func ParseAddresses(value string) []mail.Address {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	addrs, err := addressParser.ParseList(value)
	if err == nil {
		out := make([]mail.Address, len(addrs))
		for i, addr := range addrs {
			out[i] = *addr
		}
		return out
	}
	return lenientAddresses(value)
}

func lenientAddresses(value string) []mail.Address {
	var out []mail.Address
	for _, field := range strings.Split(value, ",") {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		if addr, err := addressParser.Parse(field); err == nil {
			out = append(out, *addr)
			continue
		}
		if colon := strings.LastIndexByte(field, ':'); colon >= 0 && !strings.Contains(field[:colon], "@") {
			field = strings.TrimSpace(field[colon+1:])
		}
		name := ""
		spec := field
		if open := strings.IndexByte(field, '<'); open >= 0 {
			name = strings.Trim(strings.TrimSpace(field[:open]), `"`)
			spec = field[open+1:]
			if close := strings.IndexByte(spec, '>'); close >= 0 {
				spec = spec[:close]
			}
		}
		spec = strings.TrimSpace(spec)
		if !strings.Contains(spec, "@") {
			continue
		}
		out = append(out, mail.Address{Name: decodeWords(name), Address: spec})
	}
	return out
}

var dateZones = map[string]string{
	"UT": "+0000", "UTC": "+0000", "GMT": "+0000", "Z": "+0000",
	"AST": "-0400", "ADT": "-0300", "EST": "-0500", "EDT": "-0400",
	"CST": "-0600", "CDT": "-0500", "MST": "-0700", "MDT": "-0600",
	"PST": "-0800", "PDT": "-0700",
}

var dateLayouts = [...]string{
	time.RFC3339,
	"2006-01-02 15:04:05",
	"2006-01-02",
	"2 Jan 2006 15:04:05 -0700",
	"2 Jan 2006 15:04 -0700",
	"2 Jan 2006 15:04:05",
	"2 Jan 2006",
	"2 January 2006 15:04:05 -0700",
	"2 January 2006 15:04 -0700",
	"2 January 2006 15:04:05",
	"2 January 2006",
	"Jan 2, 2006 15:04:05 -0700",
	"Jan 2, 2006 15:04:05",
	"Jan 2, 2006",
	"January 2, 2006 15:04:05 -0700",
	"January 2, 2006 15:04:05",
	"January 2, 2006",
}

func ParseDate(value string) (float64, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	if open := strings.IndexByte(value, '('); open > 0 {
		value = strings.TrimSpace(value[:open])
	}
	value = numericZone(value)
	if t, err := mail.ParseDate(value); err == nil {
		return float64(t.Unix()), true
	}
	for _, layout := range dateLayouts {
		if t, err := time.Parse(layout, value); err == nil {
			return float64(t.Unix()), true
		}
	}
	return 0, false
}

func numericZone(value string) string {
	space := strings.LastIndexByte(value, ' ')
	if space < 0 {
		return value
	}
	zone := strings.ToUpper(strings.TrimSpace(value[space+1:]))
	offset, ok := dateZones[zone]
	if !ok {
		return value
	}
	return value[:space+1] + offset
}

func FoldHeader(name, value, linesep string, utf8 bool) string {
	value = strings.NewReplacer("\r", " ", "\n", " ").Replace(value)
	if !utf8 && !isASCII(value) {
		value = mime.QEncoding.Encode("utf-8", value)
	}
	var b strings.Builder
	line := name + ": "
	for i, word := range foldWords(value) {
		if i > 0 && len(line)+len(word) > 78 {
			b.WriteString(strings.TrimRight(line, " "))
			b.WriteString(linesep)
			line = " " + word
			continue
		}
		line += word
	}
	b.WriteString(strings.TrimRight(line, " "))
	b.WriteString(linesep)
	return b.String()
}

func foldWords(value string) []string {
	var out []string
	start := 0
	for i := range len(value) {
		if value[i] == ' ' {
			out = append(out, value[start:i+1])
			start = i + 1
		}
	}
	if start < len(value) {
		out = append(out, value[start:])
	}
	return out
}

func isASCII(value string) bool {
	for i := range len(value) {
		if value[i] >= 0x80 {
			return false
		}
	}
	return true
}

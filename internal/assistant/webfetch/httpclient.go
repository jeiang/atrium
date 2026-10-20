package webfetch

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"

	"github.com/jeiang/atrium/internal/py"
)

const (
	maxLine             = 65536
	maxHeaders          = 100
	maxInterimResponses = 100
	continueStatus      = 100
	noContentStatus     = 204
	notModifiedStatus   = 304
	remoteDisconnected  = "Remote end closed connection without response"
	chunkLeftNone       = int64(-1)
	lengthNone          = int64(-1)
)

type httpException string

func (e httpException) Error() string { return string(e) }

func lineTooLong(kind string) error {
	return httpException(fmt.Sprintf("got more than %d bytes when reading %s", maxLine, kind))
}

func incompleteRead(read, expected int) error {
	if expected < 0 {
		return httpException(fmt.Sprintf("IncompleteRead(%d bytes read)", read))
	}
	return httpException(fmt.Sprintf("IncompleteRead(%d bytes read, %d more expected)", read, expected))
}

func latin1(b []byte) string {
	ascii := true
	for _, c := range b {
		if c >= 0x80 {
			ascii = false
			break
		}
	}
	if ascii {
		return string(b)
	}
	rs := make([]rune, len(b))
	for i, c := range b {
		rs[i] = rune(c)
	}
	return string(rs)
}

type headerField struct{ name, value string }

type headers []headerField

func (h headers) first(name string) (string, bool) {
	for _, f := range h {
		if strings.EqualFold(f.name, name) {
			return f.value, true
		}
	}
	return "", false
}

func (h headers) joined(name string) (string, bool) {
	var values []string
	for _, f := range h {
		if strings.EqualFold(f.name, name) {
			values = append(values, f.value)
		}
	}
	if values == nil {
		return "", false
	}
	return strings.Join(values, ", "), true
}

func readLine(br *bufio.Reader) ([]byte, error) {
	var line []byte
	for len(line) < maxLine+1 {
		c, err := br.ReadByte()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return line, nil
			}
			return nil, err
		}
		line = append(line, c)
		if c == '\n' {
			break
		}
	}
	return line, nil
}

func isBlankLine(line []byte) bool {
	s := string(line)
	return s == "\r\n" || s == "\n" || s == ""
}

func readHeaderLines(br *bufio.Reader) ([][]byte, error) {
	var lines [][]byte
	for {
		line, err := readLine(br)
		if err != nil {
			return nil, err
		}
		if len(line) > maxLine {
			return nil, lineTooLong("header line")
		}
		lines = append(lines, line)
		if len(lines) > maxHeaders {
			return nil, httpException(fmt.Sprintf("got more than %d headers", maxHeaders))
		}
		if isBlankLine(line) {
			return lines, nil
		}
	}
}

func isHeaderNameChar(c byte) bool { return c >= 0x21 && c <= 0x39 || c >= 0x3b && c <= 0x7e }

func isHeaderLine(line string) bool {
	if strings.HasPrefix(line, "From ") || line[0] == ' ' || line[0] == '\t' {
		return true
	}
	j := 0
	for j < len(line) && isHeaderNameChar(line[j]) {
		j++
	}
	return j < len(line) && line[j] == ':'
}

func splitKeepEnds(text string) []string {
	var out []string
	start := 0
	for i := 0; i < len(text); i++ {
		switch {
		case text[i] == '\r' && i+1 < len(text) && text[i+1] == '\n':
			out = append(out, text[start:i+2])
			start = i + 2
			i++
		case text[i] == '\r' || text[i] == '\n':
			out = append(out, text[start:i+1])
			start = i + 1
		}
	}
	if start < len(text) {
		out = append(out, text[start:])
	}
	return out
}

func parseHeaders(lines [][]byte) headers {
	var text strings.Builder
	for _, l := range lines {
		text.WriteString(latin1(l))
	}
	var block []string
	for _, line := range splitKeepEnds(text.String()) {
		if line[0] == '\r' || line[0] == '\n' || !isHeaderLine(line) {
			break
		}
		block = append(block, line)
	}
	var out headers
	var current []string
	flush := func() {
		if current == nil {
			return
		}
		name, first, _ := strings.Cut(current[0], ":")
		value := strings.TrimLeft(first+strings.Join(current[1:], ""), " \t\r\n")
		out = append(out, headerField{name: name, value: strings.TrimRight(value, "\r\n")})
		current = nil
	}
	for _, line := range block {
		if line[0] == ' ' || line[0] == '\t' {
			if current != nil {
				current = append(current, line)
			}
			continue
		}
		flush()
		if strings.HasPrefix(line, "From ") || line[0] == ':' {
			continue
		}
		current = []string{line}
	}
	flush()
	return out
}

func splitFields(s string, maxsplit int) []string {
	var out []string
	rest := strings.TrimLeftFunc(s, py.IsSpace)
	for rest != "" && len(out) < maxsplit {
		i := strings.IndexFunc(rest, py.IsSpace)
		if i < 0 {
			out = append(out, rest)
			return out
		}
		out = append(out, rest[:i])
		rest = strings.TrimLeftFunc(rest[i:], py.IsSpace)
	}
	if rest != "" {
		out = append(out, rest)
	}
	return out
}

func pyInt(text string, base int) (value int64, ok bool) {
	text = py.Strip(text)
	neg := false
	if text != "" && (text[0] == '+' || text[0] == '-') {
		neg = text[0] == '-'
		text = text[1:]
	}
	if base == 16 && len(text) > 2 && text[0] == '0' && (text[1] == 'x' || text[1] == 'X') {
		text = strings.TrimPrefix(text[2:], "_")
	}
	if text == "" || text[0] == '_' || text[len(text)-1] == '_' || strings.Contains(text, "__") {
		return 0, false
	}
	digits := strings.ReplaceAll(text, "_", "")
	v, err := strconv.ParseInt(digits, base, 64)
	if err != nil {
		var numErr *strconv.NumError
		if errors.As(err, &numErr) && errors.Is(numErr.Err, strconv.ErrRange) {
			v = 1<<63 - 1
		} else {
			return 0, false
		}
	}
	if neg {
		v = -v
	}
	return v, true
}

type response struct {
	status  int
	headers headers
	br      *bufio.Reader

	chunked   bool
	chunkLeft int64
	length    int64
	done      bool
}

func readResponse(br *bufio.Reader) (*response, error) {
	var status int
	var version string
	for n := 0; ; n++ {
		if n >= maxInterimResponses {
			return nil, httpException(fmt.Sprintf("got more than %d interim responses", maxInterimResponses))
		}
		line, err := readLine(br)
		if err != nil {
			return nil, err
		}
		if len(line) > maxLine {
			return nil, lineTooLong("status line")
		}
		if len(line) == 0 {
			return nil, httpException(remoteDisconnected)
		}
		text := latin1(line)
		fields := splitFields(text, 2)
		switch {
		case len(fields) >= 2:
			version = fields[0]
		default:
			version = ""
		}
		if !strings.HasPrefix(version, "HTTP/") {
			return nil, httpException(text)
		}
		code, ok := pyInt(fields[1], 10)
		if !ok || code < 100 || code > 999 {
			return nil, httpException(text)
		}
		status = int(code)
		if status != continueStatus {
			break
		}
		if _, err := readHeaderLines(br); err != nil {
			return nil, err
		}
	}
	if version != "HTTP/1.0" && version != "HTTP/0.9" && !strings.HasPrefix(version, "HTTP/1.") {
		return nil, httpException(version)
	}
	lines, err := readHeaderLines(br)
	if err != nil {
		return nil, err
	}
	r := &response{status: status, headers: parseHeaders(lines), br: br, chunkLeft: chunkLeftNone, length: lengthNone}
	if te, ok := r.headers.first("transfer-encoding"); ok && strings.ToLower(te) == "chunked" {
		r.chunked = true
	}
	if cl, ok := r.headers.first("content-length"); ok && cl != "" && !r.chunked {
		if n, ok := pyInt(cl, 10); ok && n >= 0 {
			r.length = n
		}
	}
	if status == noContentStatus || status == notModifiedStatus || status >= 100 && status < 200 {
		r.length = 0
	}
	return r, nil
}

func pyHexChunkSize(line []byte) (int64, bool) { return pyInt(latin1(line), 16) }

func (r *response) readFull(n int) ([]byte, error) {
	buf := make([]byte, n)
	got, err := io.ReadFull(r.br, buf)
	if err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, incompleteRead(got, n-got)
		}
		return nil, err
	}
	return buf, nil
}

func (r *response) discardTrailer() error {
	for trailers := 0; ; {
		line, err := readLine(r.br)
		if err != nil {
			return err
		}
		if len(line) > maxLine {
			return lineTooLong("trailer line")
		}
		if isBlankLine(line) {
			return nil
		}
		if trailers++; trailers > maxHeaders {
			return httpException(fmt.Sprintf("got more than %d trailers", maxHeaders))
		}
	}
}

func (r *response) getChunkLeft() (int64, error) {
	if r.chunkLeft > 0 {
		return r.chunkLeft, nil
	}
	if r.chunkLeft == 0 {
		if _, err := r.readFull(2); err != nil {
			return 0, err
		}
	}
	line, err := readLine(r.br)
	if err != nil {
		return 0, err
	}
	if len(line) > maxLine {
		return 0, lineTooLong("chunk size")
	}
	if i := strings.IndexByte(string(line), ';'); i >= 0 {
		line = line[:i]
	}
	size, ok := pyHexChunkSize(line)
	if !ok || size < 0 {
		r.done = true
		return 0, incompleteRead(0, -1)
	}
	if size == 0 {
		if err := r.discardTrailer(); err != nil {
			return 0, err
		}
		r.done = true
		r.chunkLeft = chunkLeftNone
		return chunkLeftNone, nil
	}
	r.chunkLeft = size
	return size, nil
}

func (r *response) read(p []byte) (int, error) {
	if r.done {
		return 0, nil
	}
	if r.chunked {
		left, err := r.getChunkLeft()
		if err != nil {
			return 0, err
		}
		if left == chunkLeftNone {
			return 0, nil
		}
		n := min(int64(len(p)), left)
		got, err := r.br.Read(p[:n])
		r.chunkLeft -= int64(got)
		if got == 0 {
			if err != nil && !errors.Is(err, io.EOF) {
				return 0, err
			}
			return 0, incompleteRead(0, -1)
		}
		return got, nil
	}
	n := int64(len(p))
	if r.length != lengthNone && n > r.length {
		n = r.length
	}
	got, err := r.br.Read(p[:n])
	if got == 0 && n > 0 {
		if err != nil && !errors.Is(err, io.EOF) {
			return 0, err
		}
		r.done = true
		return 0, nil
	}
	if r.length != lengthNone {
		r.length -= int64(got)
		if r.length == 0 {
			r.done = true
		}
	}
	return got, nil
}

func osText(err error) string {
	if text, ok := py.OSErrorText(err); ok {
		return text
	}
	var he httpException
	if errors.As(err, &he) {
		return string(he)
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, net.ErrClosed) {
		return remoteDisconnected
	}
	if text := err.Error(); text != "" {
		return text
	}
	return "OSError"
}

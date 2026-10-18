package imapclient

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"time"
)

// Error is an imaplib exception: Kind is its class name (error, abort or readonly), Msg its text.
type Error struct {
	Kind string
	Msg  string
}

func (e *Error) Error() string { return e.Msg }

func pyError(format string, a ...any) *Error {
	return &Error{Kind: "error", Msg: fmt.Sprintf(format, a...)}
}

func pyAbort(format string, a ...any) *Error {
	return &Error{Kind: "abort", Msg: fmt.Sprintf(format, a...)}
}

func pyReadonly(format string, a ...any) *Error {
	return &Error{Kind: "readonly", Msg: fmt.Sprintf(format, a...)}
}

var errTimedOut = errors.New("timed out")

// datum is one imaplib response fragment: bytes, a (bytes, literal) tuple, or None.
type datum struct {
	text  []byte
	lit   []byte
	tuple bool
	none  bool
}

type kind int

const (
	untagged kind = iota
	tagged
	continuation
)

// response is one server response as imaplib's _get_response reads it.
type response struct {
	kind  kind
	line  []byte
	typ   string
	dat   []byte
	frags []datum
	code  string
	data  datum
	coded bool
}

// store is imaplib's untagged_responses.
type store map[string][]datum

func (s store) add(typ string, d datum) {
	if d.none {
		d = datum{text: []byte{}}
	}
	s[typ] = append(s[typ], d)
}

func (s store) pop(typ string) ([]datum, bool) {
	d, ok := s[typ]
	delete(s, typ)
	return d, ok
}

// idleResponse is one entry of imaplib's _idle_responses.
type idleResponse struct {
	typ  string
	data []datum
}

func line(buf []byte, pos int) ([]byte, int, bool, error) {
	i := bytes.IndexByte(buf[pos:], '\n')
	if i < 0 {
		return nil, 0, false, nil
	}
	raw := buf[pos : pos+i+1]
	if !bytes.HasSuffix(raw, []byte("\r\n")) {
		return nil, 0, false, pyAbort("socket error: unterminated line: %s", bytesRepr(raw))
	}
	return raw[:len(raw)-2], pos + i + 1, true, nil
}

func isUpperOrDash(c byte) bool { return c >= 'A' && c <= 'Z' || c == '-' }

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// typeAndData matches `(?P<type>[A-Z-]+)( (?P<data>.*))?` at the start of b.
func typeAndData(b []byte) (string, []byte, bool) {
	n := 0
	for n < len(b) && isUpperOrDash(b[n]) {
		n++
	}
	if n == 0 {
		return "", nil, false
	}
	if n < len(b) && b[n] == ' ' {
		return string(b[:n]), b[n+1:], true
	}
	return string(b[:n]), nil, true
}

// parseUntagged applies imaplib's Untagged_response, then Untagged_status, to a line starting with "* ".
func parseUntagged(resp []byte) (string, []byte, bool) {
	rest := resp[2:]
	if typ, data, ok := typeAndData(rest); ok {
		if data == nil {
			data = []byte{}
		}
		return typ, data, true
	}
	n := 0
	for n < len(rest) && isDigit(rest[n]) {
		n++
	}
	if n == 0 || n >= len(rest) || rest[n] != ' ' {
		return "", nil, false
	}
	typ, data2, ok := typeAndData(rest[n+1:])
	if !ok {
		return "", nil, false
	}
	dat := bytes.Clone(rest[:n])
	if len(data2) > 0 {
		dat = append(append(dat, ' '), data2...)
	}
	return typ, dat, true
}

// parseTagged matches imaplib's tagre `(?P<tag>TAGPRE\d+) (?P<type>[A-Z]+) (?P<data>.*)`, the prefix being letters.
func parseTagged(resp []byte) (string, []byte, bool) {
	letters := 0
	for letters < len(resp) && resp[letters] >= 'A' && resp[letters] <= 'Z' {
		letters++
	}
	sp := letters
	for sp < len(resp) && isDigit(resp[sp]) {
		sp++
	}
	if letters == 0 || sp == letters || sp >= len(resp) || resp[sp] != ' ' {
		return "", nil, false
	}
	rest := resp[sp+1:]
	n := 0
	for n < len(rest) && rest[n] >= 'A' && rest[n] <= 'Z' {
		n++
	}
	if n == 0 || n >= len(rest) || rest[n] != ' ' {
		return "", nil, false
	}
	return string(rest[:n]), rest[n+1:], true
}

// literalSize matches imaplib's Literal `.*{(?P<size>\d+)}$`.
func literalSize(dat []byte) (int, bool) {
	if !bytes.HasSuffix(dat, []byte("}")) {
		return 0, false
	}
	open := bytes.LastIndexByte(dat, '{')
	if open < 0 || open+1 == len(dat)-1 {
		return 0, false
	}
	digits := dat[open+1 : len(dat)-1]
	for _, c := range digits {
		if !isDigit(c) {
			return 0, false
		}
	}
	n, err := strconv.Atoi(string(digits))
	return n, err == nil
}

// parenDepth is imaplib's _paren_depth: parenthesis nesting outside quoted strings.
func parenDepth(data []byte, depth int) int {
	for i := 0; i < len(data); i++ {
		switch data[i] {
		case '"':
			j := i + 1
			for j < len(data) && data[j] != '"' {
				if data[j] == '\\' {
					if j+1 >= len(data) {
						break
					}
					j++
				}
				j++
			}
			if j < len(data) && data[j] == '"' {
				i = j
			}
		case '(':
			depth++
		case ')':
			depth--
		}
	}
	return depth
}

// responseCode matches imaplib's Response_code `\[(?P<type>[A-Z-]+)( (?P<data>.*))?\]` at the start of dat.
func responseCode(dat []byte) (string, datum, bool) {
	if len(dat) == 0 || dat[0] != '[' {
		return "", datum{}, false
	}
	rest := dat[1:]
	n := 0
	for n < len(rest) && isUpperOrDash(rest[n]) {
		n++
	}
	if n == 0 || n >= len(rest) {
		return "", datum{}, false
	}
	typ := string(rest[:n])
	if rest[n] == ' ' {
		if end := bytes.LastIndexByte(rest[n+1:], ']'); end >= 0 {
			return typ, datum{text: bytes.Clone(rest[n+1 : n+1+end])}, true
		}
	}
	if rest[n] == ']' {
		return typ, datum{none: true}, true
	}
	return "", datum{}, false
}

// readResponse parses one complete response at the start of buf; n is 0 when buf holds only part of one.
func readResponse(buf []byte) (response, int, error) {
	resp, pos, ok, err := line(buf, 0)
	for ok && err == nil && len(resp) == 0 {
		resp, pos, ok, err = line(buf, pos)
	}
	if !ok || err != nil {
		return response{}, 0, err
	}
	r := response{line: bytes.Clone(resp)}
	switch {
	case len(resp) >= 2 && resp[0] == '*' && resp[1] == ' ':
		typ, dat, ok := parseUntagged(resp)
		if !ok {
			return r, 0, pyAbort("unexpected response: %s", bytesRepr(resp))
		}
		r.kind, r.typ = untagged, typ
		depth := 0
		for {
			size, lit := literalSize(dat)
			if !lit {
				break
			}
			if pos+size > len(buf) {
				return response{}, 0, nil
			}
			r.frags = append(r.frags, datum{text: dat, lit: bytes.Clone(buf[pos : pos+size]), tuple: true})
			pos += size
			depth = parenDepth(dat, depth)
			var next []byte
			if next, pos, ok, err = line(buf, pos); !ok || err != nil {
				return response{}, 0, err
			}
			for len(next) == 0 && depth > 0 {
				if next, pos, ok, err = line(buf, pos); !ok || err != nil {
					return response{}, 0, err
				}
			}
			dat = bytes.Clone(next)
		}
		r.dat = dat
		r.frags = append(r.frags, datum{text: dat})
	case len(resp) >= 1 && resp[0] == '+':
		r.kind = continuation
		return r, pos, nil
	default:
		typ, dat, ok := parseTagged(resp)
		if !ok {
			return r, 0, pyAbort("unexpected response: %s", bytesRepr(resp))
		}
		r.kind, r.typ, r.dat = tagged, typ, bytes.Clone(dat)
	}
	if r.typ == "OK" || r.typ == "NO" || r.typ == "BAD" {
		r.code, r.data, r.coded = responseCode(r.dat)
	}
	return r, pos, nil
}

// tap records every byte go-imap reads from the connection, so the responses can be parsed as imaplib does.
type tap struct {
	net.Conn
	arrived chan struct{}

	mu     sync.Mutex
	buf    []byte
	err    error
	killed error
	last   time.Time
	paused bool
}

func newTap(c net.Conn) *tap {
	return &tap{Conn: c, arrived: make(chan struct{}, 1), last: time.Now()}
}

func (t *tap) Read(p []byte) (int, error) {
	n, err := t.Conn.Read(p)
	t.mu.Lock()
	t.buf = append(t.buf, p[:n]...)
	if n > 0 {
		t.last = time.Now()
	}
	if err != nil && t.err == nil {
		t.err = err
	}
	t.mu.Unlock()
	t.signal()
	return n, err
}

func (t *tap) signal() {
	select {
	case t.arrived <- struct{}{}:
	default:
	}
}

func (t *tap) kill(reason error) {
	t.mu.Lock()
	if t.killed == nil {
		t.killed = reason
	}
	t.mu.Unlock()
	_ = t.Close()
	t.signal()
}

func (t *tap) touch(paused bool) {
	t.mu.Lock()
	t.last, t.paused = time.Now(), paused
	t.mu.Unlock()
}

// idleFor reports how long the connection has been silent while a reply is due.
func (t *tap) idleFor() (time.Duration, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return time.Since(t.last), !t.paused
}

// next parses and consumes one buffered response; ok is false when none is complete. failure is the
// connection's error, if any.
func (t *tap) next() (r response, ok bool, failure error, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	r, n, err := readResponse(t.buf)
	if err != nil {
		return r, false, nil, err
	}
	if n > 0 {
		t.buf = t.buf[n:]
		if len(t.buf) == 0 {
			t.buf = nil
		}
		return r, true, nil, nil
	}
	switch {
	case t.killed != nil:
		return r, false, t.killed, nil
	case t.err != nil && (errors.Is(t.err, io.EOF) || errors.Is(t.err, net.ErrClosed)):
		return r, false, io.EOF, nil
	}
	return r, false, t.err, nil
}

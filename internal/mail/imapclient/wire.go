package imapclient

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jeiang/atrium/internal/py"
)

type Error struct {
	Kind string
	Msg  string
}

func (e *Error) Error() string { return e.Msg }

func (e *Error) PyClass() string { return e.Kind }

func (e *Error) PyModule() string { return "imaplib.IMAP4" }

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

type response struct {
	kind  kind
	tag   string
	line  []byte
	typ   string
	dat   []byte
	frags []datum
	code  string
	data  datum
	coded bool
}

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
		return nil, 0, false, pyAbort("socket error: unterminated line: %s", py.ReprBytes(raw))
	}
	return raw[:len(raw)-2], pos + i + 1, true, nil
}

func isUpperOrDash(c byte) bool { return c >= 'A' && c <= 'Z' || c == '-' }

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

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

func parseTagged(resp []byte) (string, string, []byte, bool) {
	letters := 0
	for letters < len(resp) && resp[letters] >= 'A' && resp[letters] <= 'Z' {
		letters++
	}
	sp := letters
	for sp < len(resp) && isDigit(resp[sp]) {
		sp++
	}
	if letters == 0 || sp == letters || sp >= len(resp) || resp[sp] != ' ' {
		return "", "", nil, false
	}
	rest := resp[sp+1:]
	n := 0
	for n < len(rest) && rest[n] >= 'A' && rest[n] <= 'Z' {
		n++
	}
	if n == 0 || n >= len(rest) || rest[n] != ' ' {
		return "", "", nil, false
	}
	return string(resp[:sp]), string(rest[:n]), rest[n+1:], true
}

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
	if err != nil {
		return math.MaxInt, true
	}
	return n, true
}

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
			return r, 0, pyAbort("unexpected response: %s", py.ReprBytes(resp))
		}
		r.kind, r.typ = untagged, typ
		depth := 0
		for {
			size, lit := literalSize(dat)
			if !lit {
				break
			}
			if size > maxBuffered {
				return response{}, 0, pyAbort("socket error: literal of %d bytes exceeds %d", size, maxBuffered)
			}
			if size > len(buf)-pos {
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
		tag, typ, dat, ok := parseTagged(resp)
		if !ok {
			return r, 0, pyAbort("unexpected response: %s", py.ReprBytes(resp))
		}
		r.kind, r.tag, r.typ, r.dat = tagged, tag, typ, bytes.Clone(dat)
	}
	if r.typ == "OK" || r.typ == "NO" || r.typ == "BAD" {
		r.code, r.data, r.coded = responseCode(r.dat)
	}
	return r, pos, nil
}

const maxBuffered = 256 << 20

type sentCommand struct {
	tag, name string
	done      bool
}

type sentEnd struct{ index, end int }

type tap struct {
	net.Conn
	arrived chan struct{}

	mu      sync.Mutex
	buf     []byte
	err     error
	killed  error
	last    time.Time
	window  time.Duration
	writing int
	sent    []sentCommand
	wline   []byte
	wskip   int
	wcont   bool
	wcur    int
	ends    []sentEnd
	limit   int

	writeTimeout time.Duration
}

func newTap(c net.Conn, writeTimeout time.Duration) *tap {
	return &tap{Conn: c, arrived: make(chan struct{}, 1), last: time.Now(), limit: maxBuffered, writeTimeout: writeTimeout}
}

func (t *tap) Read(p []byte) (int, error) {
	n, err := t.Conn.Read(p)
	t.mu.Lock()
	full := len(t.buf)+n > t.limit
	if !full {
		t.buf = append(t.buf, p[:n]...)
	}
	if n > 0 {
		t.last = time.Now()
	}
	if err != nil && t.err == nil {
		t.err = err
	}
	t.mu.Unlock()
	if full {
		t.kill(fmt.Errorf("server sent more than %d unread bytes", t.limit))
	}
	t.signal()
	return n, err
}

func (t *tap) Write(p []byte) (int, error) {
	t.mu.Lock()
	t.ends = t.ends[:0]
	t.record(p)
	t.writing++
	t.mu.Unlock()
	t.signal()
	if t.writeTimeout > 0 {
		_ = t.SetWriteDeadline(time.Now().Add(t.writeTimeout))
	}
	n, err := t.Conn.Write(p)
	if t.writeTimeout > 0 {
		_ = t.SetWriteDeadline(time.Time{})
	}
	t.mu.Lock()
	for _, e := range t.ends {
		if e.end > n {
			t.sent[e.index].done = false
		}
	}
	t.writing--
	if t.window == 0 {
		t.last = time.Now()
	}
	t.mu.Unlock()
	var timeout net.Error
	if errors.As(err, &timeout) && timeout.Timeout() {
		t.kill(errTimedOut)
	}
	return n, err
}

func (t *tap) record(p []byte) {
	off := 0
	for off < len(p) {
		if t.wskip > 0 {
			k := min(t.wskip, len(p)-off)
			t.wskip -= k
			off += k
			continue
		}
		i := bytes.IndexByte(p[off:], '\n')
		if i < 0 {
			t.wline = append(t.wline, p[off:]...)
			return
		}
		line := bytes.TrimSuffix(append(t.wline, p[off:off+i]...), []byte("\r"))
		off += i + 1
		if !t.wcont {
			t.wcur = -1
			if tag, name, ok := commandTag(line); ok {
				t.sent = append(t.sent, sentCommand{tag: tag, name: name})
				t.wcur = len(t.sent) - 1
			}
		}
		t.wskip, t.wcont = clientLiteral(line)
		if !t.wcont && t.wcur >= 0 {
			t.sent[t.wcur].done = true
			t.ends = append(t.ends, sentEnd{t.wcur, off})
		}
		t.wline = t.wline[:0]
	}
}

func commandTag(line []byte) (string, string, bool) {
	sp := bytes.IndexByte(line, ' ')
	if sp < 2 || line[0] != 'T' {
		return "", "", false
	}
	for _, c := range line[1:sp] {
		if !isDigit(c) {
			return "", "", false
		}
	}
	name, _, _ := bytes.Cut(line[sp+1:], []byte(" "))
	return string(line[:sp]), string(name), len(name) > 0
}

func clientLiteral(line []byte) (int, bool) {
	if !bytes.HasSuffix(line, []byte("}")) {
		return 0, false
	}
	open := bytes.LastIndexByte(line, '{')
	if open < 0 {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSuffix(string(line[open+1:len(line)-1]), "+"))
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

func (t *tap) target(name string, from int) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	for i := len(t.sent) - 1; i >= from; i-- {
		if t.sent[i].name == name {
			return i
		}
	}
	return -1
}

func (t *tap) written(name string, from int) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	for i := len(t.sent) - 1; i >= from; i-- {
		if t.sent[i].name == name {
			return t.sent[i].done
		}
	}
	return false
}

func (t *tap) sentIndex(tag string, from int) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	for i := from; i < len(t.sent); i++ {
		if t.sent[i].tag == tag {
			return i
		}
	}
	return -1
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

// touch restarts the silence clock; window is how long the server may then stay silent, zero for the default.
func (t *tap) touch(window time.Duration) {
	t.mu.Lock()
	t.last, t.window = time.Now(), window
	t.mu.Unlock()
}

func (t *tap) idleFor(def time.Duration) (idle time.Duration, watching bool, window time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()
	window = t.window
	if window == 0 {
		window = def
	}
	return time.Since(t.last), t.writing == 0, window
}

func (t *tap) failure() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	switch {
	case t.killed != nil:
		return t.killed
	case t.err != nil && (errors.Is(t.err, io.EOF) || errors.Is(t.err, net.ErrClosed)):
		return io.EOF
	}
	return t.err
}

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

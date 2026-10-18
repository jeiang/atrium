// Package imapscript is a scripted wire-level IMAP server for tests: it replays a transcript against a real
// go-imap client over a TLS-less loopback listener and fails the test on the first client line that does not
// match. go-imap's imapmemserver has no CONDSTORE, so the mail tests use this instead.
//
// A script is a text file of lines, blank lines and `#` comments ignored:
//
//	S: line      the server sends line and CRLF
//	C: line      the client must send exactly line
//	C/ pattern   the whole client line must match pattern (Python re syntax, compiled by internal/rx)
//	! sleep 50ms the server pauses
//	! close      the server drops the connection; it ends the connection's section
//	! accept     the following steps serve the next accepted connection
//
// `{t}` in a C: or C/ line matches the client's command tag (one atom); in an S: line it is replaced by the tag
// of the last client line that had one. `{gmail.greeting}`, `{gmail.caps}`, `{gmail.authcaps}` and the same
// `icloud.` names in an S: line expand to the Gmail and ICloud profiles. A client line ending in a literal
// (`{6}` or `{6+}`) is matched alone; the next C: or C/ line is the literal's bytes and the rest of the
// command, so a sync literal needs an `S: + ...` between them; literal bytes with CR or LF need a C/ pattern
// (`\r\n`).
// When a section ends without `! close`, the server half-closes and fails on any further client bytes.
//
// go-imap client quirks the scripts encode:
//
//   - A greeting, LOGIN or AUTHENTICATE OK without `[CAPABILITY ...]` drops the known capabilities, and the
//     client sends CAPABILITY from a goroutine at once, racing the test's next command. After the greeting,
//     calling Caps first fixes the order (Gmail's greeting has no list, iCloud's has). After LOGIN it does
//     not, since Wait returns before the reset, so a script's LOGIN OK carries the list, as both servers send.
//   - FETCH items are written in map iteration order, which is random: match them with a C/ pattern such as
//     `\(UID( (FLAGS|MODSEQ)){2}\)`. The FETCH modifiers (`(CHANGEDSINCE n)`) follow in fixed order.
//   - A number set is normalised: UIDSetNum(101, 102) goes out as `101:102`, UIDSet{{1, 0}} as `1:*`.
//   - A mailbox name is quoted, except INBOX.
//   - A string is quoted, `"` and `\` backslash-escaped, when it has at most 4096 bytes, no NUL, CR or LF
//     and no byte above 0x7F (above 0x7F is fine once UTF8=ACCEPT is enabled). Any other string is a
//     literal: `{n+}` when the server advertises LITERAL+, or LITERAL- with n <= 4096, otherwise a sync
//     `{n}` that waits for the server's `+`. Before LOGIN neither server advertises either, so a non-ASCII
//     password is a sync literal.
//   - A SEARCH with a non-ASCII string, X-GM-RAW included, gets `CHARSET UTF-8` unless UTF8=ACCEPT is
//     enabled, in which case the string is quoted UTF-8 and no CHARSET is sent.
package imapscript

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/jeiang/atrium/internal/rx"
)

const cleanupTimeout = 5 * time.Second

type kind int

const (
	send kind = iota
	expect
	expectPattern
	sleep
	hangUp
)

type step struct {
	kind  kind
	pos   string
	line  string
	re    *rx.Regexp
	tag   bool
	delay time.Duration
}

// Script is a parsed transcript: one list of steps per connection.
type Script struct {
	name  string
	conns [][]step
}

// Parse reads a script; name prefixes the positions in its errors.
func Parse(name, text string) (*Script, error) {
	s := &Script{name: name, conns: [][]step{nil}}
	closed := false
	for n, line := range strings.Split(text, "\n") {
		line = strings.TrimSuffix(line, "\r")
		pos := name + ":" + strconv.Itoa(n+1)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if line == "! accept" {
			s.conns = append(s.conns, nil)
			closed = false
			continue
		}
		if closed {
			return nil, fmt.Errorf("%s: step after ! close", pos)
		}
		st, err := parseStep(pos, line)
		if err != nil {
			return nil, err
		}
		closed = st.kind == hangUp
		last := len(s.conns) - 1
		s.conns[last] = append(s.conns[last], st)
	}
	for i, c := range s.conns {
		if len(c) == 0 {
			return nil, fmt.Errorf("%s: connection %d has no steps", name, i+1)
		}
	}
	return s, nil
}

func parseStep(pos, line string) (step, error) {
	st := step{pos: pos}
	switch {
	case strings.HasPrefix(line, "S: "):
		expanded, err := expand(line[3:])
		if err != nil {
			return st, fmt.Errorf("%s: %w", pos, err)
		}
		st.kind, st.line = send, expanded
	case strings.HasPrefix(line, "C: "):
		if strings.Count(line, "{t}") > 1 {
			return st, fmt.Errorf("%s: more than one {t}", pos)
		}
		st.kind, st.line = expect, line[3:]
	case strings.HasPrefix(line, "C/ "):
		if strings.Count(line, "{t}") > 1 {
			return st, fmt.Errorf("%s: more than one {t}", pos)
		}
		re, err := rx.Compile(strings.Replace(line[3:], "{t}", `(?P<t>\S+)`, 1), 0)
		if err != nil {
			return st, fmt.Errorf("%s: %w", pos, err)
		}
		st.kind, st.line, st.re, st.tag = expectPattern, line[3:], re, strings.Contains(line, "{t}")
	case line == "! close":
		st.kind = hangUp
	case strings.HasPrefix(line, "! sleep "):
		d, err := time.ParseDuration(line[len("! sleep "):])
		if err != nil {
			return st, fmt.Errorf("%s: %w", pos, err)
		}
		st.kind, st.delay = sleep, d
	default:
		return st, fmt.Errorf("%s: unknown script line %q", pos, line)
	}
	return st, nil
}

// expand replaces the `{profile.key}` placeholders; `{t}` and literal markers have no dot and stay.
func expand(line string) (string, error) {
	var b strings.Builder
	for {
		open := strings.IndexByte(line, '{')
		if open < 0 {
			b.WriteString(line)
			return b.String(), nil
		}
		end := strings.IndexByte(line[open:], '}')
		if end < 0 {
			b.WriteString(line)
			return b.String(), nil
		}
		name := line[open+1 : open+end]
		b.WriteString(line[:open])
		if strings.Contains(name, ".") && !strings.ContainsAny(name, " ()\"") {
			v, ok := vars[name]
			if !ok {
				return "", fmt.Errorf("unknown placeholder {%s}", name)
			}
			b.WriteString(v)
		} else {
			b.WriteString(line[open : open+end+1])
		}
		line = line[open+end+1:]
	}
}

// Load parses the script at path or fails the test.
func Load(t testing.TB, path string) *Script {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s, err := Parse(path, string(b))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// MustParse parses an inline script or fails the test.
func MustParse(t testing.TB, text string) *Script {
	t.Helper()
	s, err := Parse(t.Name(), text)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// Server replays a Script on a loopback listener, one section per accepted connection, in accept order.
type Server struct {
	script *Script
	ln     net.Listener
	done   chan struct{}

	mu    sync.Mutex
	conns []net.Conn
	errs  []error
}

// Serve starts a Server for s. At cleanup it stops accepting, waits for every section and reports each
// mismatch, unconsumed step and missing connection with t.Errorf.
func Serve(t testing.TB, s *Script) *Server {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{script: s, ln: ln, done: make(chan struct{})}
	go srv.run()
	t.Cleanup(func() {
		_ = ln.Close()
		select {
		case <-srv.done:
		case <-time.After(cleanupTimeout):
			srv.mu.Lock()
			for _, c := range srv.conns {
				_ = c.Close()
			}
			srv.mu.Unlock()
			<-srv.done
		}
		if err := srv.err(); err != nil {
			t.Errorf("imapscript %s: %v", s.name, err)
		}
	})
	return srv
}

// Addr is the listener's host:port.
func (srv *Server) Addr() string { return srv.ln.Addr().String() }

// Dial connects a go-imap client to the server; cleanup closes it before the server's cleanup runs.
func (srv *Server) Dial(t testing.TB, options *imapclient.Options) *imapclient.Client {
	t.Helper()
	conn, err := net.Dial("tcp", srv.Addr())
	if err != nil {
		t.Fatal(err)
	}
	c := imapclient.New(conn, options)
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func (srv *Server) run() {
	var wg sync.WaitGroup
	defer close(srv.done)
	defer wg.Wait()
	for i, steps := range srv.script.conns {
		conn, err := srv.ln.Accept()
		if err != nil {
			srv.fail(fmt.Errorf("connection %d not accepted: %w", i+1, err))
			return
		}
		srv.mu.Lock()
		srv.conns = append(srv.conns, conn)
		srv.mu.Unlock()
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := replay(conn, steps); err != nil {
				srv.fail(fmt.Errorf("connection %d: %w", i+1, err))
			}
		}()
	}
}

func (srv *Server) fail(err error) {
	srv.mu.Lock()
	srv.errs = append(srv.errs, err)
	srv.mu.Unlock()
}

func (srv *Server) err() error {
	srv.mu.Lock()
	defer srv.mu.Unlock()
	return errors.Join(srv.errs...)
}

func replay(conn net.Conn, steps []step) error {
	defer func() { _ = conn.Close() }()
	r := bufio.NewReader(conn)
	tag := ""
	literal := -1
	for _, st := range steps {
		switch st.kind {
		case send:
			line := strings.ReplaceAll(st.line, "{t}", tag)
			if _, err := io.WriteString(conn, line+"\r\n"); err != nil {
				return fmt.Errorf("%s: write %q: %w", st.pos, line, err)
			}
		case sleep:
			time.Sleep(st.delay)
		case hangUp:
			return nil
		case expect, expectPattern:
			line, err := readLine(r, literal)
			if err != nil {
				return fmt.Errorf("%s: want client line %q: %w", st.pos, st.line, err)
			}
			got, ok := st.match(line)
			if !ok {
				return fmt.Errorf("%s: client sent %q, want %q", st.pos, line, st.line)
			}
			if got != "" {
				tag = got
			}
			literal = literalSize(line)
		}
	}
	if tc, ok := conn.(*net.TCPConn); ok {
		_ = tc.CloseWrite()
	}
	line, err := readLine(r, literal)
	if errors.Is(err, io.EOF) && line == "" {
		return nil
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("after the script: %w", err)
	}
	return fmt.Errorf("client sent %q after the script ended", line)
}

// match reports whether line matches the step and returns the tag it captured.
func (st step) match(line string) (string, bool) {
	if st.kind == expectPattern {
		m, err := st.re.FullMatch(line)
		if err != nil || m == nil {
			return "", false
		}
		if !st.tag {
			return "", true
		}
		tag, _ := m.Named("t")
		return tag, true
	}
	before, after, hasTag := strings.Cut(st.line, "{t}")
	if !hasTag {
		return "", line == st.line
	}
	if !strings.HasPrefix(line, before) || !strings.HasSuffix(line, after) || len(line) <= len(before)+len(after) {
		return "", false
	}
	tag := line[len(before) : len(line)-len(after)]
	if strings.ContainsAny(tag, " \t") {
		return "", false
	}
	return tag, true
}

// readLine reads one client line without its CRLF; literal >= 0 means the line starts with that many
// literal bytes.
func readLine(r *bufio.Reader, literal int) (string, error) {
	var b strings.Builder
	if literal > 0 {
		buf := make([]byte, literal)
		n, err := io.ReadFull(r, buf)
		b.Write(buf[:n])
		if err != nil {
			return b.String(), err
		}
	}
	rest, err := r.ReadString('\n')
	b.WriteString(rest)
	line := b.String()
	if err != nil {
		return line, err
	}
	return strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r"), nil
}

// literalSize returns n for a line ending in `{n}` or `{n+}`, else -1.
func literalSize(line string) int {
	if !strings.HasSuffix(line, "}") {
		return -1
	}
	open := strings.LastIndexByte(line, '{')
	if open < 0 {
		return -1
	}
	n, err := strconv.Atoi(strings.TrimSuffix(line[open+1:len(line)-1], "+"))
	if err != nil || n < 0 {
		return -1
	}
	return n
}

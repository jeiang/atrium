package imapscript_test

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/testutil/imapscript"
)

type recorder struct {
	testing.TB
	errs     []string
	cleanups []func()
}

func (r *recorder) Helper()                   {}
func (r *recorder) Cleanup(f func())          { r.cleanups = append(r.cleanups, f) }
func (r *recorder) Errorf(f string, a ...any) { r.errs = append(r.errs, fmt.Sprintf(f, a...)) }

func (r *recorder) finish() string {
	for i := len(r.cleanups) - 1; i >= 0; i-- {
		r.cleanups[i]()
	}
	return strings.Join(r.errs, "\n")
}

func TestParseErrors(t *testing.T) {
	cases := map[string]string{
		"S: a\nX: b":          `x:2: unknown script line "X: b"`,
		"S: a\n! close\nS: b": "x:3: step after ! close",
		"S: a\n! accept":      "x: connection 2 has no steps",
		"S: {gmail.nope}":     "x:1: unknown placeholder {gmail.nope}",
		"C: {t} A {t}":        "x:1: more than one {t}",
		"C/ (":                "x:1: ",
		"S: a\n! sleep soon":  "x:2: ",
	}
	for text, want := range cases {
		_, err := imapscript.Parse("x", text)
		if err == nil || !strings.HasPrefix(err.Error(), want) {
			t.Errorf("Parse(%q): got %v, want prefix %q", text, err, want)
		}
	}
}

func TestPlaceholders(t *testing.T) {
	srv := imapscript.Serve(t, imapscript.MustParse(t, "S: {icloud.greeting}\nC: {t} NOOP\nS: {t} OK [x {3}] {t}"))
	conn, r := dial(t, srv)
	if got := readLine(t, r); got != imapscript.ICloud.Greeting {
		t.Fatalf("greeting: got %q", got)
	}
	write(t, conn, "a1 NOOP\r\n")
	if got := readLine(t, r); got != "a1 OK [x {3}] a1" {
		t.Fatalf("reply: got %q", got)
	}
}

func TestFailures(t *testing.T) {
	cases := []struct {
		name, script, client, want string
	}{
		{"mismatch", "S: * OK\nC: {t} NOOP", "a1 LOGOUT\r\n", `connection 1: x:2: client sent "a1 LOGOUT", want "{t} NOOP"`},
		{"pattern", "C/ {t} UID FETCH \\d+", "a1 UID FETCH 1:2\r\n", `connection 1: x:1: client sent "a1 UID FETCH 1:2"`},
		{"empty tag", "C: {t} NOOP", " NOOP\r\n", `connection 1: x:1: client sent " NOOP"`},
		{"unconsumed", "C: {t} NOOP\nC: {t} LOGOUT", "a1 NOOP\r\n", `connection 1: x:2: want client line "{t} LOGOUT": EOF`},
		{"extra", "C: {t} NOOP", "a1 NOOP\r\na2 NOOP\r\n", `connection 1: client sent "a2 NOOP" after the script ended`},
		{"missing connection", "C: {t} NOOP\n! accept\nS: * OK", "a1 NOOP\r\n", "connection 2 not accepted"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, err := imapscript.Parse("x", c.script)
			if err != nil {
				t.Fatal(err)
			}
			rec := &recorder{TB: t}
			srv := imapscript.Serve(rec, s)
			conn, err := net.Dial("tcp", srv.Addr())
			if err != nil {
				t.Fatal(err)
			}
			write(t, conn, c.client)
			_ = conn.(*net.TCPConn).CloseWrite()
			_, _ = io.Copy(io.Discard, conn)
			_ = conn.Close()
			if got := rec.finish(); !strings.Contains(got, c.want) {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestLiterals(t *testing.T) {
	script := `S: * OK ready
C: {t} LOGIN "u" {10}
S: + go ahead
C: pässwörd
S: {t} OK done
C: {t} APPEND INBOX {10+}
C/ line one\r\n
! sleep 10ms
S: {t} OK appended
! close`
	srv := imapscript.Serve(t, imapscript.MustParse(t, script))
	conn, r := dial(t, srv)
	readLine(t, r)
	write(t, conn, "a1 LOGIN \"u\" {10}\r\n")
	if got := readLine(t, r); got != "+ go ahead" {
		t.Fatalf("continuation: got %q", got)
	}
	write(t, conn, "pässwörd\r\n")
	if got := readLine(t, r); got != "a1 OK done" {
		t.Fatalf("login: got %q", got)
	}
	write(t, conn, "a2 APPEND INBOX {10+}\r\nline one\r\n\r\n")
	if got := readLine(t, r); got != "a2 OK appended" {
		t.Fatalf("append: got %q", got)
	}
	if _, err := r.ReadString('\n'); err != io.EOF {
		t.Fatalf("after ! close: got %v, want EOF", err)
	}
}

func dial(t *testing.T, srv *imapscript.Server) (net.Conn, *bufio.Reader) {
	t.Helper()
	conn, err := net.Dial("tcp", srv.Addr())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn, bufio.NewReader(conn)
}

func readLine(t *testing.T, r *bufio.Reader) string {
	t.Helper()
	line, err := r.ReadString('\n')
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return strings.TrimSuffix(line, "\r\n")
}

func write(t *testing.T, conn net.Conn, s string) {
	t.Helper()
	if _, err := io.WriteString(conn, s); err != nil {
		t.Fatalf("write: %v", err)
	}
}

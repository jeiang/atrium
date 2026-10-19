package imapclient

import (
	"bufio"
	"bytes"
	"context"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/testutil/imapscript"
)

func TestIdleReissueKeepsRepliesInStep(t *testing.T) {
	ctx := context.Background()
	client, c := icloud(t, icloudLogin+`C: {t} EXAMINE INBOX
S: {t} OK [READ-ONLY] EXAMINE completed
C: {t} IDLE
S: + idling
# go-imap re-issues IDLE: DONE and the next IDLE go out before the first IDLE's reply.
C: DONE
C: {t} IDLE
S: * 6 EXISTS
S: T3 OK IDLE terminated
S: + idling
C: DONE
S: T4 OK IDLE terminated
C: {t} EXAMINE "Missing"
S: {t} NO [NONEXISTENT] Unknown
`+logout, live)
	c.idleRestart = 100 * time.Millisecond
	must(t, client.Connect(ctx))
	must(t, client.Select(ctx, "INBOX"))
	changed, err := client.IdleChanges(ctx, time.Minute)
	if err != nil || !changed {
		t.Fatalf("IdleChanges = %v, %v; want the EXISTS", changed, err)
	}
	wantError(t, client.Select(ctx, "Missing"), "error", `IMAP command failed: NO [b'[NONEXISTENT] Unknown']`)
	client.Close()
}

func TestUnexpectedTaggedResponse(t *testing.T) {
	client, _ := icloud(t, icloudLogin+`C: {t} NOOP
# go-imap closes the connection on a tag it never sent; a line written after that meets a closed socket and
# the client answers with a reset, so the section ends here.
S: T77 OK stray
! close
`, live)
	must(t, client.Connect(context.Background()))
	wantError(t, client.Noop(context.Background()), "abort", `command: NOOP => unexpected tagged response: b'T77 OK stray'`)
}

func TestHugeLiteralIsRefused(t *testing.T) {
	for _, size := range []string{"9223372036854775800", "99999999999999999999999", "268435457"} {
		_, _, err := readResponse([]byte("* 1 FETCH (UID 1 BODY[TEXT] {" + size + "}\r\nabc"))
		if e, ok := err.(*Error); !ok || e.Kind != "abort" || !strings.HasPrefix(e.Msg, "socket error: literal of ") {
			t.Errorf("literal {%s}: err = %v", size, err)
		}
	}
	if r, n, err := readResponse([]byte("* 1 FETCH (UID 1 BODY[TEXT] {5}\r\nab")); err != nil || n != 0 {
		t.Fatalf("partial literal: %+v, %d, %v", r, n, err)
	}
}

func TestTapBufferIsBounded(t *testing.T) {
	server, clientSide := net.Pipe()
	defer func() { _ = server.Close() }()
	tp := newTap(clientSide)
	tp.limit = 16
	go func() { _, _ = server.Write(bytes.Repeat([]byte("x"), 32)) }()
	buf := make([]byte, 64)
	for tp.failure() == nil {
		if _, err := tp.Read(buf); err != nil {
			break
		}
	}
	if err := tp.failure(); err == nil || !strings.Contains(err.Error(), "more than 16 unread bytes") {
		t.Fatalf("failure = %v", err)
	}
	tp.mu.Lock()
	defer tp.mu.Unlock()
	if len(tp.buf) > 16 {
		t.Fatalf("buffered %d bytes", len(tp.buf))
	}
}

func TestPanicInAReplyClosesTheConnection(t *testing.T) {
	client, c := icloud(t, icloudLogin, live)
	must(t, client.Connect(context.Background()))
	err := c.await(context.Background(), func() error { panic("boom") })
	if err == nil || !strings.Contains(err.Error(), "panic") {
		t.Fatalf("err = %v", err)
	}
	if c.tap.failure() == nil {
		t.Fatal("the connection must be closed after a panic")
	}
}

// slowConn delays writes that carry the APPEND literal.
type slowConn struct {
	net.Conn
	delay time.Duration
}

func (s slowConn) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte("Subject")) {
		time.Sleep(s.delay)
	}
	return s.Conn.Write(p)
}

func TestWatchdogIgnoresTimeSpentWriting(t *testing.T) {
	srv := imapscript.Serve(t, imapscript.MustParse(t, icloudLogin+`C/ ^{t} APPEND INBOX \(\\Seen\) "[^"]+" \{9\+\}$
C: Subject:x
S: {t} OK APPEND completed
`+logout))
	c := newConn("imap.test", "atrium.test@icloud.com", "app-specific password")
	c.dial = func(ctx context.Context) (net.Conn, error) {
		var d net.Dialer
		nc, err := d.DialContext(ctx, "tcp", srv.Addr())
		return slowConn{nc, 400 * time.Millisecond}, err
	}
	c.readTimeout = 150 * time.Millisecond
	gated := mail.Gated(c, live)
	t.Cleanup(gated.Close)
	must(t, gated.Connect(context.Background()))
	must(t, gated.Append(context.Background(), "INBOX", []byte("Subject:x")))
	gated.Close()
}

// FuzzReadResponse feeds arbitrary server bytes through the imaplib reader and the imap.py parsers.
func FuzzReadResponse(f *testing.F) {
	for _, path := range []string{"testdata/icloud.txt", "testdata/gmail.txt"} {
		file, err := os.Open(path)
		if err != nil {
			f.Fatal(err)
		}
		var server bytes.Buffer
		sc := bufio.NewScanner(file)
		for sc.Scan() {
			if line, ok := strings.CutPrefix(sc.Text(), "S: "); ok {
				server.WriteString(strings.ReplaceAll(line, "{t}", "T1") + "\r\n")
			}
		}
		_ = file.Close()
		f.Add(server.Bytes())
	}
	f.Add([]byte("* 1 FETCH (BODY[TEXT] {9223372036854775800}\r\n"))
	f.Add([]byte("* 1 FETCH (UID 5 BODYSTRUCTURE ((\"a\" \"b\" (\"name\" \"\\\"x\") NIL)) {3}\r\nabc\r\n\r\n)\r\n"))
	f.Fuzz(func(t *testing.T, buf []byte) {
		for len(buf) > 0 {
			r, n, err := readResponse(buf)
			if err != nil || n == 0 {
				return
			}
			if n > len(buf) {
				t.Fatalf("consumed %d of %d bytes", n, len(buf))
			}
			buf = buf[n:]
			switch r.typ {
			case "FETCH":
				_, _ = parseFetch(r.frags)
			case "LIST":
				_, _ = parseList(r.frags)
			case "STATUS":
				_, _ = parseStatus(r.frags)
			case "SEARCH":
				_, _ = parseSearch(r.frags)
			}
		}
	})
}

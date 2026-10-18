package smtp_test

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/md5"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"math/big"
	"net"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/mail/smtp"
)

const host = "smtp.test"

func certificate(t *testing.T, name string) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: name},
		DNSNames:              []string{name},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, pool
}

// script is a scripted SMTP server: replies by verb, with the extensions it advertises before and after TLS.
type script struct {
	greeting string
	ehlo     []string
	ehloTLS  []string
	replies  map[string]string
	rcpt     map[string]string
	hangup   string
	auth     func(c *serverConn, line string)
}

type server struct {
	addr  string
	pool  *x509.CertPool
	mu    sync.Mutex
	lines []string
	data  []byte
	done  chan struct{}
}

type serverConn struct {
	conn net.Conn
	r    *bufio.Reader
	s    *server
}

func (c *serverConn) send(lines ...string) {
	for _, l := range lines {
		_, _ = c.conn.Write([]byte(l + "\r\n"))
	}
}

func (c *serverConn) read() (string, bool) {
	line, err := c.r.ReadString('\n')
	if err != nil {
		return "", false
	}
	line = strings.TrimRight(line, "\r\n")
	c.s.mu.Lock()
	c.s.lines = append(c.s.lines, line)
	c.s.mu.Unlock()
	return line, true
}

func multiline(code string, lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		sep := "-"
		if i == len(lines)-1 {
			sep = " "
		}
		out[i] = code + sep + l
	}
	return out
}

func serve(t *testing.T, sc script) *server {
	t.Helper()
	cert, pool := certificate(t, host)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &server{addr: ln.Addr().String(), pool: pool, done: make(chan struct{})}
	t.Cleanup(func() { _ = ln.Close(); <-s.done })
	if sc.greeting == "" {
		sc.greeting = "220 smtp.test ESMTP ready"
	}
	if sc.ehlo == nil {
		sc.ehlo = []string{host, "STARTTLS", "SIZE 35882577"}
	}
	if sc.ehloTLS == nil {
		sc.ehloTLS = []string{host, "AUTH LOGIN PLAIN", "SIZE 35882577"}
	}
	reply := func(verb, fallback string) string {
		if r, ok := sc.replies[verb]; ok {
			return r
		}
		return fallback
	}
	go func() {
		defer close(s.done)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		c := &serverConn{conn: conn, r: bufio.NewReader(conn), s: s}
		defer func() { _ = c.conn.Close() }()
		c.send(sc.greeting)
		if !strings.HasPrefix(sc.greeting, "220") {
			return
		}
		secure := false
		for {
			line, ok := c.read()
			if !ok {
				return
			}
			verb := strings.ToUpper(strings.Fields(line + " x")[0])
			if verb == sc.hangup {
				return
			}
			switch verb {
			case "EHLO":
				if r, ok := sc.replies["EHLO"]; ok {
					c.send(r)
				} else if secure {
					c.send(multiline("250", sc.ehloTLS)...)
				} else {
					c.send(multiline("250", sc.ehlo)...)
				}
			case "HELO":
				c.send(reply("HELO", "250 "+host))
			case "STARTTLS":
				r := reply("STARTTLS", "220 2.0.0 Ready to start TLS")
				c.send(r)
				if strings.HasPrefix(r, "220") {
					tc := tls.Server(c.conn, &tls.Config{Certificates: []tls.Certificate{cert}})
					if tc.Handshake() != nil {
						return
					}
					c.conn, c.r, secure = tc, bufio.NewReader(tc), true
				}
			case "AUTH":
				if sc.auth != nil {
					sc.auth(c, line)
				} else {
					c.send(reply("AUTH", "235 2.7.0 Accepted"))
				}
			case "MAIL":
				c.send(reply("MAIL", "250 2.1.0 OK"))
			case "RCPT":
				addr := strings.TrimSuffix(strings.TrimPrefix(line[len("rcpt to:"):], "<"), ">")
				if r, ok := sc.rcpt[addr]; ok {
					c.send(r)
				} else {
					c.send("250 2.1.5 OK")
				}
			case "DATA":
				r := reply("DATA", "354 Go ahead")
				c.send(r)
				if !strings.HasPrefix(r, "354") {
					continue
				}
				var data []byte
				for {
					l, err := c.r.ReadString('\n')
					if err != nil {
						return
					}
					if l == ".\r\n" {
						break
					}
					data = append(data, l...)
				}
				s.mu.Lock()
				s.data = data
				s.mu.Unlock()
				if sc.hangup == "DATA." {
					return
				}
				c.send(reply("DATA.", "250 2.0.0 OK queued"))
			case "RSET":
				c.send("250 2.0.0 OK")
			case "QUIT":
				c.send(reply("QUIT", "221 2.0.0 Bye"))
				return
			default:
				c.send("502 5.5.1 Unrecognized command")
			}
		}
	}()
	return s
}

// dialer connects to s whatever host smtp.Send asks for and verifies the certificate as smtp.test against roots.
func (s *server) dialer(roots *x509.CertPool) smtp.Factory {
	d := smtp.Dialer{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, s.addr)
		},
		TLSConfig: &tls.Config{RootCAs: roots},
		LocalName: "client.test",
	}
	return func(ctx context.Context, _ string, port int, timeout time.Duration) (smtp.Session, error) {
		return d.Dial(ctx, host, port, timeout)
	}
}

func (s *server) transcript() []string {
	<-s.done
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lines
}

type staticCreds struct{}

func (staticCreds) Credential(string) (string, string, bool) { return "me", "pw", true }

func message(to, cc string) *smtp.Message {
	return smtp.BuildMessage("me@icloud.com", smtp.Fields{ToAddrs: to, CcAddrs: cc, Subject: "Hi", Body: "line one\n.\n.. two\n"}, 1791280800, "<id.1@icloud.com>")
}

func sendVia(t *testing.T, s *server, msg *smtp.Message) (smtp.Refusals, error) {
	t.Helper()
	return smtp.Send(t.Context(), staticCreds{}, "icloud", msg, s.dialer(s.pool))
}

func TestSessionDeliversAndReturnsPartialRefusals(t *testing.T) {
	s := serve(t, script{rcpt: map[string]string{"b@x.test": "550 5.1.1 <b@x.test>: Recipient address rejected"}})
	msg := message("a@x.test, b@x.test", "c@x.test")
	refused, err := sendVia(t, s, msg)
	if err != nil {
		t.Fatal(err)
	}
	want := smtp.Refusals{{Addr: "b@x.test", Code: 550, Msg: "5.1.1 <b@x.test>: Recipient address rejected"}}
	if !reflect.DeepEqual(refused, want) || refused[0].Reply() != "(550, b'5.1.1 <b@x.test>: Recipient address rejected')" {
		t.Fatalf("refused %+v", refused)
	}
	flat, err := msg.Bytes("\r\n", false)
	if err != nil {
		t.Fatal(err)
	}
	wantLines := []string{
		"ehlo client.test",
		"STARTTLS",
		"ehlo client.test",
		"AUTH PLAIN " + base64.StdEncoding.EncodeToString([]byte("\x00me\x00pw")),
		"mail from:<me@icloud.com> size=" + strconv.Itoa(len(flat)),
		"rcpt to:<a@x.test>",
		"rcpt to:<b@x.test>",
		"rcpt to:<c@x.test>",
		"data",
		"QUIT",
	}
	if got := s.transcript(); !reflect.DeepEqual(got, wantLines) {
		t.Fatalf("transcript\n got %q\nwant %q", got, wantLines)
	}
	stuffed := strings.ReplaceAll(string(flat), "\r\n.", "\r\n..")
	if string(s.data) != stuffed {
		t.Fatalf("data\n got %q\nwant %q", s.data, stuffed)
	}
}

func TestSessionErrorsAreWordedAsSmtplib(t *testing.T) {
	for _, c := range []struct {
		name string
		sc   script
		want string
	}{
		{"greeting refused", script{greeting: "421 4.3.2 Service not available"}, "(421, b'4.3.2 Service not available')"},
		{"no STARTTLS", script{ehlo: []string{host, "AUTH PLAIN"}}, "STARTTLS extension not supported by server."},
		{"STARTTLS refused", script{replies: map[string]string{"STARTTLS": "454 4.7.0 TLS not available due to local problem"}}, "(454, b'4.7.0 TLS not available due to local problem')"},
		{"EHLO and HELO refused", script{replies: map[string]string{"EHLO": "500 5.5.1 no", "HELO": "501 5.5.4 Invalid EHLO hostname"}}, "(501, b'5.5.4 Invalid EHLO hostname')"},
		{"no AUTH", script{ehloTLS: []string{host, "SIZE 1000000"}}, "SMTP AUTH extension not supported by server."},
		{"no usable mechanism", script{ehloTLS: []string{host, "AUTH XOAUTH2"}}, "No suitable authentication method found."},
		{"bad credentials", script{replies: map[string]string{"AUTH": "535-5.7.8 Username and Password not accepted. For more information, go to\r\n535 5.7.8  https://support.google.com/mail/?p=BadCredentials a1"}},
			"(535, b'5.7.8 Username and Password not accepted. For more information, go to\\n5.7.8  https://support.google.com/mail/?p=BadCredentials a1')"},
		{"sender refused", script{replies: map[string]string{"MAIL": "553 5.7.1 Sender address rejected: not owned by user"}}, "(553, b'5.7.1 Sender address rejected: not owned by user', 'me@icloud.com')"},
		{"every recipient refused", script{rcpt: map[string]string{"a@x.test": "550 5.1.1 User unknown"}}, "{'a@x.test': (550, b'5.1.1 User unknown')}"},
		{"DATA refused", script{replies: map[string]string{"DATA": "554 5.5.1 Error: no valid recipients"}}, "(554, b'5.5.1 Error: no valid recipients')"},
		{"message refused", script{replies: map[string]string{"DATA.": "554 5.7.0 Message rejected as spam"}}, "(554, b'5.7.0 Message rejected as spam')"},
		{"hang-up", script{hangup: "MAIL"}, "Connection unexpectedly closed"},
		{"QUIT refused", script{replies: map[string]string{"QUIT": "500 5.5.2 what"}}, "(500, b'5.5.2 what')"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := serve(t, c.sc)
			_, err := sendVia(t, s, message("a@x.test", ""))
			var serr smtp.SendError
			if !errors.As(err, &serr) || serr.Error() != c.want {
				t.Fatalf("got %v (%T), want %q", err, err, c.want)
			}
		})
	}
}

func TestSessionAllRefusedResetsWithoutData(t *testing.T) {
	s := serve(t, script{rcpt: map[string]string{"a@x.test": "550 5.1.1 User unknown"}})
	if _, err := sendVia(t, s, message("a@x.test", "")); err == nil {
		t.Fatal("no error")
	}
	got := s.transcript()
	if got[len(got)-2] != "rset" || got[len(got)-1] != "QUIT" || s.data != nil {
		t.Fatalf("transcript %q data %q", got, s.data)
	}
}

func TestSessionVerifiesTheServerCertificate(t *testing.T) {
	s := serve(t, script{})
	_, other := certificate(t, host)
	_, err := smtp.Send(t.Context(), staticCreds{}, "icloud", message("a@x.test", ""), s.dialer(other))
	var serr smtp.SendError
	if !errors.As(err, &serr) || !strings.Contains(serr.Error(), "certificate") {
		t.Fatalf("got %v", err)
	}
	if got := s.transcript(); got[len(got)-1] != "STARTTLS" {
		t.Fatalf("spoke after a failed handshake: %q", got)
	}
}

func TestSessionConnectionRefused(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	f := smtp.Dialer{DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}}.Dial
	_, err = smtp.Send(t.Context(), staticCreds{}, "icloud", message("a@x.test", ""), f)
	if err == nil || err.Error() != "[Errno 111] Connection refused" {
		t.Fatalf("got %v", err)
	}
}

func TestSessionLoginMechanisms(t *testing.T) {
	cramChallenge := "<1896.697170952@smtp.test>"
	for _, c := range []struct {
		name  string
		mechs string
		auth  func(c *serverConn, line string)
		want  []string
	}{
		{"CRAM-MD5 first", "AUTH PLAIN CRAM-MD5 LOGIN", func(c *serverConn, line string) {
			c.send("334 " + base64.StdEncoding.EncodeToString([]byte(cramChallenge)))
			c.read()
			c.send("235 2.7.0 Accepted")
		}, func() []string {
			h := hmac.New(md5.New, []byte("pw"))
			h.Write([]byte(cramChallenge))
			return []string{"AUTH CRAM-MD5", base64.StdEncoding.EncodeToString([]byte("me " + hex.EncodeToString(h.Sum(nil))))}
		}()},
		{"LOGIN", "AUTH LOGIN", func(c *serverConn, line string) {
			c.send("334 UGFzc3dvcmQ6")
			c.read()
			c.send("235 2.7.0 Accepted")
		}, []string{"AUTH LOGIN bWU=", "cHc="}},
		{"falls back after a refusal", "AUTH CRAM-MD5 PLAIN", func(c *serverConn, line string) {
			if strings.HasPrefix(line, "AUTH CRAM-MD5") {
				c.send("535 5.7.8 no")
				return
			}
			c.send("235 2.7.0 Accepted")
		}, []string{"AUTH CRAM-MD5", "AUTH PLAIN AG1lAHB3"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := serve(t, script{ehloTLS: []string{host, c.mechs}, auth: c.auth})
			if _, err := sendVia(t, s, message("a@x.test", "")); err != nil {
				t.Fatal(err)
			}
			var auth []string
			for _, l := range s.transcript() {
				if strings.HasPrefix(l, "mail ") {
					break
				}
				if strings.HasPrefix(l, "AUTH") || len(auth) > 0 {
					auth = append(auth, l)
				}
			}
			if !reflect.DeepEqual(auth, c.want) {
				t.Fatalf("auth %q, want %q", auth, c.want)
			}
		})
	}
}

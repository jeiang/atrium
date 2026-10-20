package smtptest

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/mail/smtp"
)

const wireHost = "smtp.test"

type Wire struct {
	DropAt     string
	FinalReply string
	addr       string
	roots      *x509.CertPool
	conns      atomic.Int32
}

func NewWire(t testing.TB, dropAt, finalReply string) *Wire {
	t.Helper()
	w := &Wire{DropAt: dropAt, FinalReply: finalReply}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: wireHost},
		DNSNames:              []string{wireHost},
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
	cert := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	w.addr = ln.Addr().String()
	w.roots = x509.NewCertPool()
	w.roots.AddCert(leaf)
	if w.FinalReply == "" {
		w.FinalReply = "250 2.0.0 OK queued"
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			w.conns.Add(1)
			go w.serve(conn, cert)
		}
	}()
	return w
}

func (w *Wire) Conns() int { return int(w.conns.Load()) }

func (w *Wire) Factory(ctx context.Context, _ string, port int, timeout time.Duration) (smtp.Session, error) {
	d := smtp.Dialer{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, w.addr)
		},
		TLSConfig: &tls.Config{RootCAs: w.roots},
		LocalName: "client.test",
	}
	return d.Dial(ctx, wireHost, port, timeout)
}

func (w *Wire) serve(conn net.Conn, cert tls.Certificate) {
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	send := func(lines ...string) {
		for _, l := range lines {
			_, _ = conn.Write([]byte(l + "\r\n"))
		}
	}
	r := bufio.NewReader(conn)
	secure := false
	send("220 " + wireHost + " ESMTP ready")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		verb := strings.ToUpper(strings.Fields(line + " x")[0])
		if verb == w.DropAt {
			return
		}
		switch verb {
		case "EHLO":
			if secure {
				send("250-"+wireHost, "250 AUTH PLAIN")
			} else {
				send("250-"+wireHost, "250 STARTTLS")
			}
		case "STARTTLS":
			send("220 2.0.0 Ready to start TLS")
			tc := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{cert}})
			if tc.Handshake() != nil {
				return
			}
			conn, r, secure = tc, bufio.NewReader(tc), true
		case "AUTH":
			send("235 2.7.0 Accepted")
		case "MAIL", "RCPT", "RSET":
			send("250 2.1.0 OK")
		case "DATA":
			send("354 Go ahead")
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
			}
			if w.DropAt == "BODY" {
				return
			}
			send(w.FinalReply)
		case "QUIT":
			send("221 2.0.0 Bye")
			return
		default:
			send("502 5.5.1 Unrecognized command")
		}
	}
}

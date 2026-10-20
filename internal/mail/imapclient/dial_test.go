package imapclient

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"io"
	"math/big"
	"net"
	"testing"
	"time"
)

func listen(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	return ln
}

func TestDialTimesOutOnAnUnresponsiveListener(t *testing.T) {
	ln := listen(t)
	accepted := make(chan net.Conn, 1)
	go func() {
		if c, err := ln.Accept(); err == nil {
			accepted <- c
		}
	}()
	defer func() { _ = (<-accepted).Close() }()
	c := newConn("imap.test", "u", "p")
	c.addr, c.dialTimeout = ln.Addr().String(), 100*time.Millisecond
	start := time.Now()
	err := c.Connect(context.Background())
	if !errors.Is(err, context.DeadlineExceeded) {
		var ne net.Error
		if !errors.As(err, &ne) || !ne.Timeout() {
			t.Fatalf("err = %v, want a timeout", err)
		}
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("Connect took %v", d)
	}
}

func selfSigned(t *testing.T, host string) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: host},
		DNSNames:     []string{host},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA:         true, BasicConstraintsValid: true,
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

func serveTLS(t *testing.T, cert tls.Certificate) string {
	t.Helper()
	ln := listen(t)
	go func() {
		for {
			raw, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				c := tls.Server(raw, &tls.Config{Certificates: []tls.Certificate{cert}})
				defer func() { _ = c.Close() }()
				if c.Handshake() == nil {
					_, _ = io.WriteString(c, "* BYE verified\r\n")
				}
			}()
		}
	}()
	return ln.Addr().String()
}

func TestTLSVerifiesCertificateAndHostName(t *testing.T) {
	cert, pool := selfSigned(t, "imap.test")
	addr := serveTLS(t, cert)
	connect := func(host string, roots *x509.CertPool) error {
		c := newConn(host, "u", "p")
		c.addr = addr
		c.tlsConfig.RootCAs = roots
		return c.Connect(context.Background())
	}
	var unknown x509.UnknownAuthorityError
	if err := connect("imap.test", nil); !errors.As(err, &unknown) {
		t.Fatalf("untrusted certificate: err = %v", err)
	}
	var mismatch x509.HostnameError
	if err := connect("other.test", pool); !errors.As(err, &mismatch) {
		t.Fatalf("wrong host name: err = %v", err)
	}
	wantError(t, connect("imap.test", pool), "error", "invalid greeting: * BYE verified")
}

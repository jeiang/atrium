package webfetch

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"log"
	"math/big"
	"net"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func punycodeCert(t *testing.T, name string) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: name}, DNSNames: []string{name},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, pool
}

// An IDN host is converted to ASCII once: the lookup, the Host header and the TLS server name all carry the
// punycode form, so the certificate of the punycode name verifies.
func TestIDNHostUsesItsASCIIFormEverywhere(t *testing.T) {
	const ascii = "xn--bcher-kva.example"
	cert, roots := punycodeCert(t, ascii)
	var seenHost atomic.Value
	srv := httptest.NewUnstartedServer(handler(nil))
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, GetConfigForClient: func(h *tls.ClientHelloInfo) (*tls.Config, error) {
		seenHost.Store(h.ServerName)
		return nil, nil
	}}
	srv.StartTLS()
	defer srv.Close()
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	var looked atomic.Value
	f := &Fetcher{
		Allowed:  onlyLoopback,
		tlsRoots: roots,
		Lookup: func(_ context.Context, host string) ([]netip.Addr, error) {
			looked.Store(host)
			return []netip.Addr{loopback}, nil
		},
	}
	p, err := f.FetchPage(t.Context(), "https://bücher.example:"+port+"/ok")
	if err != nil || !strings.Contains(string(p.Body), "calibration") {
		t.Fatalf("fetch: %v", err)
	}
	if looked.Load() != ascii || seenHost.Load() != ascii {
		t.Errorf("lookup %v, SNI %v, want %s", looked.Load(), seenHost.Load(), ascii)
	}
}

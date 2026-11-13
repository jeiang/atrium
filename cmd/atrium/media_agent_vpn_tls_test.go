package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func vpnCertificate(t *testing.T) (string, string, *x509.CertPool) {
	t.Helper()
	dir := t.TempDir()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: vpnHost}, DNSNames: []string{vpnHost},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	cert, keyFile := filepath.Join(dir, "vpn.crt"), filepath.Join(dir, "vpn.key")
	if os.WriteFile(cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600) != nil ||
		os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: raw}), 0o600) != nil {
		t.Fatal("cannot write the VPN certificate")
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(parsed)
	return cert, keyFile, pool
}

func TestVPNListenerOverTLSServesRangesOverHTTP1(t *testing.T) {
	t.Parallel()
	addr := freeLoopbackAddr(t)
	cert, key, pool := vpnCertificate(t)
	agent := startMediaAgent(t, mediaAgentOpts{extra: vpnTable(addr, "127.0.0.1/32") + "tls_cert = " + strconv.Quote(cert) + "\ntls_key = " + strconv.Quote(key) + "\n"})
	body := strings.Repeat("abcdefghij", 10)
	writeMediaFile(t, filepath.Join(agent.roots["movies"], "Film (2020)", "Film (2020).mkv"), body)
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: vpnHost}, ForceAttemptHTTP2: true}}
	t.Cleanup(client.CloseIdleConnections)
	var resp *http.Response
	var got []byte
	waitFor(t, func() bool {
		req, err := http.NewRequest(http.MethodGet, "https://"+addr+"/library/movies/Film%20%282020%29/Film%20%282020%29.mkv", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = vpnHost
		req.Header.Set("Range", "bytes=10-19")
		resp, err = client.Do(req)
		if err != nil {
			return false
		}
		defer func() { _ = resp.Body.Close() }()
		got, err = io.ReadAll(resp.Body)
		return err == nil
	})
	if resp.StatusCode != http.StatusPartialContent || string(got) != body[10:20] {
		t.Fatalf("ranged GET over TLS = %d %q", resp.StatusCode, got)
	}
	if resp.ProtoMajor != 1 || resp.TLS == nil || resp.TLS.NegotiatedProtocol == "h2" {
		t.Fatalf("the VPN listener negotiated %s %v, want HTTP/1.1 only", resp.Proto, resp.TLS)
	}
}

func TestRefusedVPNPeersCannotExhaustTheListener(t *testing.T) {
	t.Parallel()
	probe, err := net.Listen("tcp", "127.0.0.2:0")
	if err != nil {
		t.Skipf("needs a second loopback address (127.0.0.2): %v", err)
	}
	_ = probe.Close()
	addr := freeLoopbackAddr(t)
	agent := startMediaAgent(t, mediaAgentOpts{extra: vpnTable(addr, "127.0.0.1/32")})
	writeMediaFile(t, filepath.Join(agent.roots["movies"], "Film", "film.mkv"), "film bytes")
	file := "http://" + addr + "/library/movies/Film/film.mkv"
	waitFor(t, func() bool {
		code, _ := vpnRequest(t, http.MethodHead, file, vpnHost, nil)
		return code == http.StatusOK
	})
	dialer := net.Dialer{LocalAddr: &net.TCPAddr{IP: net.ParseIP("127.0.0.2")}, Timeout: 5 * time.Second}
	var held []net.Conn
	t.Cleanup(func() {
		for _, c := range held {
			_ = c.Close()
		}
	})
	for i := range 300 {
		conn, err := dialer.Dial("tcp", addr)
		if err != nil {
			if errors.Is(err, syscall.EADDRNOTAVAIL) {
				t.Skipf("cannot dial from 127.0.0.2: %v", err)
			}
			t.Fatalf("refused peer connection %d: %v", i, err)
		}
		held = append(held, conn)
	}
	for i, conn := range held {
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		if n, err := conn.Read(make([]byte, 1)); n != 0 || errors.Is(err, os.ErrDeadlineExceeded) {
			t.Fatalf("refused peer connection %d was kept open: %d %v", i, n, err)
		}
	}
	start := time.Now()
	if code, got := vpnRequest(t, http.MethodGet, file, vpnHost, nil); code != http.StatusOK || got != "film bytes" || time.Since(start) > 5*time.Second {
		t.Fatalf("allowed peer after 300 refused connections = %d %q in %s", code, got, time.Since(start))
	}
}

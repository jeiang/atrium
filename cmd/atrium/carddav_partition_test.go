package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"html"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/davmirror"
)

const (
	frontHost     = "contacts.dav.localhost"
	partitionHost = "p7-contacts.dav.localhost"
	foreignHost   = "contacts.elsewhere.localhost"
)

type partitionedDAV struct {
	*httptest.Server
	port             string
	mu               sync.Mutex
	redirectTo       string
	partition        string
	rootHome         bool
	partitionRefuses bool
	refuseTimes      int
	requests         []string
	seen             map[string]int
	authorized       map[string]int
}

const davChallenge = `X-MobileMe-AuthToken realm="Newcastle", Basic realm="Newcastle"`

func (p *partitionedDAV) set(change func(*partitionedDAV)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	change(p)
}

func (p *partitionedDAV) origin(host string) string { return "https://" + host + ":" + p.port }

func (p *partitionedDAV) count(host string) (seen, authorized int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.seen[host], p.authorized[host]
}

func (p *partitionedDAV) multistatus(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(http.StatusMultiStatus)
	_, _ = fmt.Fprintf(w, `<?xml version="1.0" encoding="utf-8"?><D:multistatus xmlns:D="DAV:" xmlns:CR="urn:ietf:params:xml:ns:carddav">%s</D:multistatus>`, body)
}

func davResponse(href, prop string) string {
	return "<D:response><D:href>" + href + "</D:href><D:propstat><D:prop>" + prop + "</D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>"
}

func (p *partitionedDAV) serve(w http.ResponseWriter, r *http.Request) {
	host, _, _ := net.SplitHostPort(r.Host)
	user, pass, ok := r.BasicAuth()
	good := ok && user == "operator@icloud.invalid" && pass == davPassword
	p.mu.Lock()
	p.seen[host]++
	p.requests = append(p.requests, r.Method+" "+host+" "+r.URL.Path)
	if good {
		p.authorized[host]++
	}
	redirect, partition, rootHome, partitionRefuses := p.redirectTo, p.partition, p.rootHome, p.partitionRefuses
	if good && host == partitionHost && p.refuseTimes > 0 {
		p.refuseTimes--
		partitionRefuses = true
	}
	p.mu.Unlock()
	if !good {
		w.Header().Set("WWW-Authenticate", davChallenge)
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	switch {
	case host == frontHost && r.URL.Path == "/":
		if partition != "" {
			w.Header().Set("X-Apple-User-Partition", partition)
		}
		w.Header().Set("Set-Cookie", `X-APPLE-WEBAUTH-USER="v=1:s=0:d=1234567890"; Path=/`)
		prop := `<D:current-user-principal><D:href>/42/principal/</D:href></D:current-user-principal>`
		if rootHome {
			prop += `<CR:addressbook-home-set><D:href>/42/carddavhome/</D:href></CR:addressbook-home-set>`
		}
		p.multistatus(w, davResponse("/", prop))
	case host == frontHost && r.URL.Path == "/.well-known/carddav":
		w.Header().Set("Location", p.origin(redirect)+"/42/principal/")
		w.WriteHeader(http.StatusMovedPermanently)
	case host == frontHost:
		w.Header().Set("X-Apple-User-Partition", "3")
		w.Header().Set("WWW-Authenticate", davChallenge)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, "<html><body>account 1234567890 for operator@icloud.invalid</body></html>")
	case host == partitionHost && partitionRefuses:
		w.WriteHeader(http.StatusUnauthorized)
	case r.URL.Path == "/42/principal/":
		p.multistatus(w, davResponse(r.URL.Path, `<CR:addressbook-home-set><D:href>`+p.origin(host)+`/42/carddavhome/</D:href></CR:addressbook-home-set>`))
	case r.Method == "PROPFIND" && r.URL.Path == "/42/carddavhome/":
		p.multistatus(w, davResponse("/42/carddavhome/", `<D:resourcetype><D:collection/></D:resourcetype>`)+
			davResponse("/42/carddavhome/card/", `<D:resourcetype><D:collection/><CR:addressbook/></D:resourcetype>`))
	case r.Method == "REPORT" && r.URL.Path == "/42/carddavhome/card/":
		p.multistatus(w, davResponse("/42/carddavhome/card/alice.vcf", `<D:getetag>"1"</D:getetag><CR:address-data>`+html.EscapeString(aliceCard)+`</CR:address-data>`))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func partitionedDAVServer(t *testing.T, dir string) (*partitionedDAV, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: frontHost},
		DNSNames:  []string{"*.dav.localhost", foreignHost},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true, IsCA: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	ca := filepath.Join(dir, "dav-ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	p := &partitionedDAV{redirectTo: partitionHost, partition: "7", rootHome: true, seen: map[string]int{}, authorized: map[string]int{}}
	p.Server = httptest.NewUnstartedServer(http.HandlerFunc(p.serve))
	p.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
	p.StartTLS()
	t.Cleanup(p.Close)
	_, p.port, _ = net.SplitHostPort(p.Listener.Addr().String())
	return p, ca
}

func connectProxy(t *testing.T, target string) string {
	t.Helper()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		upstream, err := net.Dial("tcp", target)
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		conn, buf, err := w.(http.Hijacker).Hijack()
		if err != nil {
			_ = upstream.Close()
			return
		}
		_, _ = conn.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n"))
		go func() {
			_, _ = io.Copy(upstream, buf)
			_ = upstream.Close()
		}()
		go func() {
			_, _ = io.Copy(conn, upstream)
			_ = conn.Close()
		}()
	}))
	t.Cleanup(proxy.Close)
	return proxy.URL
}

func partitionedICloud(t *testing.T) (string, *partitionedDAV) {
	t.Helper()
	requireCipher(t)
	setEnv(t, "ATRIUM_DB_KEY", rightKeyHex)
	dir, cal := davFixture(t)
	dav, ca := partitionedDAVServer(t, dir)
	host := accountsFile(t, dir, mailAccount{"icloud", "icloud", "operator@icloud.invalid", davPassword, false})
	f, err := os.OpenFile(host, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintf(f, `
[connections."icloud/icloud"]
kind = "icloud"
label = "iCloud DAV"
capabilities = ["caldav", "carddav"]
credential_files = { "mail/icloud/password" = %q }
transport = { ca_file = %q, proxy = %q }
`, filepath.Join(dir, "icloud.password"), ca, connectProxy(t, dav.Listener.Addr().String())); err != nil {
		t.Fatal(err)
	}
	setEnv(t, "ATRIUM_HOST_CONFIG", host)
	front := dav.origin(frontHost) + "/"
	setEnv(t, davmirror.CardDAVEnv, front)
	t.Setenv(davmirror.CardDAVEnv, front)
	t.Setenv(davmirror.CalDAVEnv, cal.URL+"/")
	return dir, dav
}

func TestCardDAVFindsAPartitionedAddressBookAndNeverSendsTheCredentialToAForeignHost(t *testing.T) {
	dir, dav := partitionedICloud(t)

	carddav := func(view icloudTest) (state, reason string) {
		for _, r := range view.Results {
			if r.Capability == davmirror.CapabilityCardDAV {
				return r.State, r.Reason
			}
		}
		return "", ""
	}

	view := testICloudConnection(t)
	if state, reason := carddav(view); state != "ok" || reason != "1 collection" {
		t.Fatalf("carddav with a home set on the refusing front host = %s %q, want ok with 1 collection (%+v)", state, reason, view)
	}
	if got := runApp(t, "contacts", "sync"); got.code != cli.ExitOK || !strings.Contains(got.stdout, `"written":1`) {
		t.Fatalf("contacts sync with a home set on the refusing front host: %d %q %q", got.code, got.stdout, got.stderr)
	}
	if _, err := os.Stat(filepath.Join(dir, "contacts", "card", "alice.vcf")); err != nil {
		t.Fatalf("the partitioned address book was not mirrored: %v", err)
	}
	if _, authorized := dav.count(partitionHost); authorized == 0 {
		t.Fatal("the partition host received no authenticated request")
	}

	dav.set(func(p *partitionedDAV) { p.rootHome = false })
	view = testICloudConnection(t)
	if state, reason := carddav(view); state != "ok" || reason != "1 collection" {
		t.Fatalf("carddav behind a refused principal = %s %q, want ok with 1 collection (%+v)", state, reason, view)
	}

	dav.set(func(p *partitionedDAV) { p.partitionRefuses = true })
	view = testICloudConnection(t)
	want := "iCloud accepted the app password but refused the principal request on the partition host (HTTP 401, no WWW-Authenticate challenge); " +
		"also refused the principal request on the configured host (HTTP 401, with a WWW-Authenticate challenge) " +
		"and the well-known request on the partition host (HTTP 401, no WWW-Authenticate challenge)"
	if state, reason := carddav(view); state != "failing" || reason != want {
		t.Errorf("carddav refused on every host = %s %q, want failing %q", state, reason, want)
	}
	if got := runApp(t, "contacts", "sync"); got.code == cli.ExitOK {
		t.Errorf("contacts sync refused on every host succeeded: %q", got.stdout)
	}

	dav.set(func(p *partitionedDAV) { p.partitionRefuses, p.partition, p.redirectTo = false, "", foreignHost })
	view = testICloudConnection(t)
	if state, reason := carddav(view); state != "failing" || !strings.Contains(reason, "well-known") || !strings.Contains(reason, foreignHost) {
		t.Errorf("carddav redirected to a foreign host = %s %q, want failing naming the well-known step and the host", state, reason)
	}
	if got := runApp(t, "contacts", "sync"); got.code == cli.ExitOK {
		t.Errorf("contacts sync redirected to a foreign host succeeded: %q", got.stdout)
	}
	if seen, authorized := dav.count(foreignHost); seen != 0 || authorized != 0 {
		t.Errorf("the foreign host received %d requests, %d authenticated, want none", seen, authorized)
	}
}

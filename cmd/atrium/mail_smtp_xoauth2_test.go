package main

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/mail/drafts"
	"github.com/jeiang/atrium/internal/settings"
)

type smtpFake struct {
	mu     sync.Mutex
	tokens []string
	data   string
}

func (f *smtpFake) handle(c net.Conn) {
	defer func() { _ = c.Close() }()
	r := bufio.NewReader(c)
	write := func(line string) { _, _ = c.Write([]byte(line + "\r\n")) }
	write("220 fake ESMTP")
	tlsOn := false
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		upper := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(upper, "EHLO"):
			write("250-fake")
			if !tlsOn {
				write("250-STARTTLS")
			}
			write("250-AUTH XOAUTH2 LOGIN PLAIN")
			write("250 OK")
		case upper == "STARTTLS":
			write("220 ready")
			tc := tls.Server(c, smtpTLSConfig)
			if err := tc.Handshake(); err != nil {
				return
			}
			c, r, tlsOn = tc, bufio.NewReader(tc), true
		case strings.HasPrefix(upper, "AUTH XOAUTH2 "):
			raw, _ := base64.StdEncoding.DecodeString(strings.TrimSpace(line[len("AUTH XOAUTH2 "):]))
			token := ""
			if _, after, ok := strings.Cut(string(raw), "auth=Bearer "); ok {
				token, _, _ = strings.Cut(after, "\x01")
			}
			f.mu.Lock()
			f.tokens = append(f.tokens, token)
			attempt := len(f.tokens)
			f.mu.Unlock()
			if attempt == 1 {
				write("334 eyJzdGF0dXMiOiI0MDAifQ==")
				if _, err := r.ReadString('\n'); err != nil {
					return
				}
				write("535 5.7.8 invalid token")
			} else {
				write("235 2.7.0 accepted")
			}
		case strings.HasPrefix(upper, "MAIL FROM:"), strings.HasPrefix(upper, "RCPT TO:"):
			write("250 OK")
		case upper == "DATA":
			write("354 go ahead")
			var body strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if strings.TrimRight(l, "\r\n") == "." {
					break
				}
				body.WriteString(l)
			}
			f.mu.Lock()
			f.data = body.String()
			f.mu.Unlock()
			write("250 queued")
		case upper == "QUIT":
			write("221 bye")
			return
		default:
			write("250 OK")
		}
	}
}

func (f *smtpFake) seen() ([]string, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.tokens...), f.data
}

var smtpTLSConfig *tls.Config

func smtpCert(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "127.0.0.1"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		IsCA:        true, BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "smtp-ca.pem")
	out, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := pem.Encode(out, &pem.Block{Type: "CERTIFICATE", Bytes: der}); err != nil {
		t.Fatal(err)
	}
	_ = out.Close()
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	smtpTLSConfig = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key, Leaf: cert}}}
	return path
}

func TestDraftSendOverXOAUTH2RefreshesTheTokenOnlyAtAuth(t *testing.T) {
	fake := &gmailTokenFake{}
	tokenSrv := httptest.NewServer(http.HandlerFunc(fake.handler))
	t.Cleanup(tokenSrv.Close)
	t.Setenv("ATRIUM_GOOGLE_TOKEN_URL", tokenSrv.URL)
	dir, _, f := moduleFixture(t, flagsExcept("", ""))
	setEnv(t, "GOOGLE_OAUTH_CLIENT_SECRET", "secret")
	setEnv(t, "GOOGLE_GMAIL_REFRESH_TOKEN", "refresh")

	ca := smtpCert(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	smtp := &smtpFake{}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go smtp.handle(c)
		}
	}()
	t.Setenv("ATRIUM_SMTP_ADDR", ln.Addr().String())
	t.Setenv("ATRIUM_SMTP_CA", ca)

	d, err := db.Connect(filepath.Join(dir, "atrium.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(context.Background(), d, filepath.Join(dir, "atrium.db")); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.Exec("INSERT INTO settings (key, value) VALUES ('google_client_id', 'client-id')"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.Exec("INSERT INTO mail_accounts (id, label, preset, login, enabled, pinned, auth_method, created_at) " +
		"VALUES ('oauth', 'OAuth', 'gmail', 'atrium.test@gmail.test', 1, 0, 'xoauth2', 0)"); err != nil {
		t.Fatal(err)
	}

	base := startServeIn(t, dir)
	cfg, err := config.Load(config.Map(f.env))
	if err != nil {
		t.Fatal(err)
	}
	store := &drafts.Store{DB: d, Cfg: cfg}
	id, err := store.Create(config.With(settings.With(context.Background(), f.settings), f.state), nil, "oauth", []string{"to@x.test"}, nil, "Hi", "body", nil)
	if err != nil {
		t.Fatal(err)
	}

	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	code, body, _ := fetch(t, client, http.MethodGet, base+"/drafts", nil)
	if code != http.StatusOK {
		t.Fatalf("drafts page: %d", code)
	}
	m := regexp.MustCompile(`name="revision" value="([0-9]+)"`).FindStringSubmatch(body)
	if m == nil {
		t.Fatal("the draft page has no revision")
	}
	code, body, _ = fetch(t, client, http.MethodPost, base+"/drafts/"+strconv.FormatInt(id, 10)+"/send", url.Values{"revision": {m[1]}})
	if code != http.StatusOK {
		t.Fatalf("send: %d %s", code, body)
	}
	tokens, data := smtp.seen()
	if len(tokens) != 2 || tokens[0] != "access-1" || tokens[1] != "access-2" {
		t.Fatalf("AUTH XOAUTH2 tokens = %v, want access-1 then access-2", tokens)
	}
	for _, want := range []string{"Subject: Hi", "To: to@x.test", "body"} {
		if !strings.Contains(data, want) {
			t.Errorf("the message lacks %q:\n%s", want, data)
		}
	}
	var status string
	if err := d.Read.QueryRow("SELECT status FROM drafts WHERE id = ?", id).Scan(&status); err != nil || status != "sent" {
		t.Fatalf("draft status = %q, %v", status, err)
	}
}

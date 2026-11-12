//go:build !modernc

package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/imapclient"
	"github.com/jeiang/atrium/internal/mail/mailtest"
	"github.com/jeiang/atrium/internal/testutil/llmstub"
)

type strictSMTP struct {
	mu    sync.Mutex
	creds map[string]string
}

func (f *strictSMTP) handle(c net.Conn) {
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
			write("250-AUTH PLAIN")
			write("250 OK")
		case upper == "STARTTLS":
			write("220 ready")
			tc := tls.Server(c, smtpTLSConfig)
			if err := tc.Handshake(); err != nil {
				return
			}
			c, r, tlsOn = tc, bufio.NewReader(tc), true
		case strings.HasPrefix(upper, "AUTH PLAIN "):
			raw, _ := base64.StdEncoding.DecodeString(strings.TrimSpace(line[len("AUTH PLAIN "):]))
			parts := strings.Split(string(raw), "\x00")
			user, password := "", ""
			if len(parts) == 3 {
				user, password = parts[1], parts[2]
			}
			f.mu.Lock()
			accepted := f.creds[user] == password
			f.mu.Unlock()
			if accepted {
				write("235 2.7.0 accepted")
			} else {
				write("535 5.7.8 authentication failed")
			}
		case upper == "QUIT":
			write("221 bye")
			return
		default:
			write("250 OK")
		}
	}
}

func strictSMTPRig(t *testing.T, creds map[string]string) {
	t.Helper()
	ca := smtpCert(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	server := &strictSMTP{creds: creds}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go server.handle(c)
		}
	}()
	t.Setenv("ATRIUM_SMTP_ADDR", ln.Addr().String())
	t.Setenv("ATRIUM_SMTP_CA", ca)
}

func strictIMAPRig(t *testing.T, creds map[string]string, fakes map[string]*mailtest.FakeClient) {
	t.Helper()
	t.Cleanup(imapclient.UseDialer(func(_ context.Context, a config.AccountSpec, password string) (mail.Conn, error) {
		if want, ok := creds[a.Login]; !ok || want != password {
			return nil, fmt.Errorf("AUTHENTICATIONFAILED Invalid credentials")
		}
		fake, ok := fakes[a.Name]
		if !ok {
			return nil, fmt.Errorf("unexpected account %q", a.Name)
		}
		return fake, nil
	}))
}

func keyedMailFixture(t *testing.T) (string, string) {
	t.Helper()
	requireCipher(t)
	setEnv(t, "ATRIUM_DB_KEY", rightKeyHex)
	t.Cleanup(func() { db.SetDefaultKey(db.Key{}) })
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	srv := llmstub.Server(t, nil)
	setEnv(t, "ATRIUM_MAIL_MAIL_CHAT_URL", srv.URL+"/v1")
	seedSettings(t, map[string]string{mail.KeyMode: "shadow"})
	return dir, srv.URL + "/v1"
}

func mailCount(t *testing.T, d *db.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := d.Read.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

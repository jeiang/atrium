package main

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/cookiejar"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jeiang/atrium/internal/config"
	connectionsapp "github.com/jeiang/atrium/internal/connections/application"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/imapclient"
	"github.com/jeiang/atrium/internal/mail/mailtest"
	"github.com/jeiang/atrium/internal/testutil/embedstub"
	"github.com/jeiang/atrium/internal/testutil/mailscript"
)

func TestAHostMailAccountWithoutAPasswordFileKeepsThePasswordEnteredByTheOperator(t *testing.T) {
	requireCipher(t)
	setEnv(t, "ATRIUM_DB_KEY", rightKeyHex)
	dir, api, f := moduleFixture(t, flagsExcept("", ""))
	api.AnswerJSON(mailscript.Load(t).JSONAnswer)
	embed := embedstub.Server(t)
	host := hostConfigBodyWith(embeddingTable(embed.URL+"/v1"), defaultWebPushConnection, filepath.Join(testRoot, "host", "anthropic.key")) +
		"\n[connections.\"mail/home\"]\nkind = \"mail\"\nlabel = \"home\"\nconfig = { preset = \"icloud\", login = \"me@icloud.test\", auth_method = \"password\", enabled = \"true\" }\ncapabilities = [\"imap\", \"smtp\"]\n"
	setEnv(t, "ATRIUM_HOST_CONFIG", writeFile(t, dir, "host.toml", host))
	seedSettings(t, map[string]string{mail.KeyMode: "shadow"})

	fake := mailtest.NewFakeClient()
	box := fake.AddBox("INBOX", mailtest.NewBox(1))
	h, x := mailtest.RawWith("Hello home", "from@example.test", "<home@example.test>", "body")
	box.Add(h, x, nil, nil)
	var mu sync.Mutex
	var seen []string
	t.Cleanup(imapclient.UseDialer(func(_ context.Context, _ config.AccountSpec, password string) (mail.Conn, error) {
		mu.Lock()
		seen = append(seen, password)
		mu.Unlock()
		if password != "home-pass" {
			return nil, fmt.Errorf("AUTHENTICATIONFAILED Invalid credentials")
		}
		return fake.Conn(), nil
	}))
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

	d := keyedDB(t, filepath.Join(dir, "atrium.db"))
	stored := func() string {
		t.Helper()
		return queryValue(t, d.Read, "SELECT COUNT(*) FROM connection_credentials WHERE connection_id = 'mail/home' AND slot = 'mail/home/password' AND source = 'database' AND value = 'home-pass'")
	}
	sync := func() {
		t.Helper()
		app := newAppWith(f.lookup, imapclient.Opener, nil, quietLog)
		var out, errs bytes.Buffer
		if code := app.Run(context.Background(), []string{"sync"}, &out, &errs); code != 0 {
			t.Fatalf("sync: %d %s", code, errs.String())
		}
	}

	token := localAPIToken(t, "host-account-password")
	base, _, stop, err := startServeAppArgs(t, f.app, &lockedBuffer{}, "--auth", "local")
	if err != nil {
		t.Fatal(err)
	}
	s := &apiSite{t: t, base: base}
	offered := map[string]bool{}
	for _, c := range strictJSON[connectionsapp.ConnectionsSchema](t, "/connections/schema", s.do(http.MethodGet, "/api/v1/connections/schema", token, nil)).Connections {
		for _, field := range c.Fields {
			if field.Secret {
				offered[string(c.ID)+" "+field.Name] = true
			}
		}
	}
	if !offered["mail/home mail/home/password"] || offered["icloud/home mail/home/password"] {
		t.Fatalf("the password field is offered on %v, want it on mail/home only", offered)
	}
	jar, _ := cookiejar.New(nil)
	if code, page, _ := fetch(t, &http.Client{Jar: jar}, http.MethodGet, base+"/settings/connections", nil); code != http.StatusOK || !strings.Contains(page, `name="credential.mail/home/password"`) {
		t.Fatalf("the connections page (%d) has no password input for mail/home", code)
	}
	apiData(t, s.do(http.MethodPatch, "/api/v1/connections/mail/home", token, map[string]any{"credentials": map[string]string{"mail/home/password": "home-pass"}}), http.StatusOK)
	if stored() != "1" {
		t.Fatal("the entered password is not stored")
	}
	stop()

	sync()
	if stored() != "1" {
		t.Fatal("a later process start dropped the entered password")
	}
	if n := queryValue(t, d.Read, "SELECT COUNT(*) FROM messages WHERE account = 'home'"); n != "1" {
		t.Fatalf("home synced %s messages, want 1", n)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) == 0 || seen[len(seen)-1] != "home-pass" {
		t.Fatalf("the dialer saw %d logins, the last without the entered password", len(seen))
	}
}

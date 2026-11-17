package main

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"sync"
	"testing"

	"github.com/jeiang/atrium/internal/apps/mail"
	"github.com/jeiang/atrium/internal/apps/mail/imapclient"
	"github.com/jeiang/atrium/internal/apps/mail/mailtest"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/testutil/embedstub"
	"github.com/jeiang/atrium/internal/testutil/mailscript"
)

func TestAHostMailConnectionAndAnAccountAddedInTheUIBothSync(t *testing.T) {
	setEnv(t, "ATRIUM_DB_KEY", rightKeyHex)
	dir, api, f := moduleFixture(t, flagsExcept("", ""))
	api.AnswerJSON(mailscript.Load(t).JSONAnswer)
	embed := embedstub.Server(t)
	setEnv(t, "ATRIUM_HOST_CONFIG", accountsFileEmbedding(t, dir, embed.URL+"/v1", filepath.Join(testRoot, "host", "anthropic.key"), mailAccount{"home", "icloud", "me@icloud.test", "home-pass", false}))
	seedSettings(t, map[string]string{mail.KeyMode: "shadow"})

	fakes := map[string]*mailtest.FakeClient{"home": mailtest.NewFakeClient(), "work": mailtest.NewFakeClient()}
	fakes["work"].Gmail = true
	for name, fake := range fakes {
		box := fake.AddBox("INBOX", mailtest.NewBox(1))
		h, x := mailtest.RawWith("Hello "+name, "from@example.test", "<"+name+"@example.test>", "body")
		box.Add(h, x, nil, nil)
	}
	all := fakes["work"].AddBox("[Gmail]/All Mail", mailtest.NewBox(2), `\All`)
	fakes["work"].AddBox("[Gmail]/Trash", mailtest.NewBox(3), `\Trash`)
	h, x := mailtest.RawWith("Standup", "lead@work.test", "<work-1@work.test>", "Moved to 2.")
	all.Add(h, x, nil, []string{`\Inbox`})

	passwords := map[string]string{"me@icloud.test": "home-pass", "me@work.test": "work-pass"}
	var mu sync.Mutex
	seen := map[string]string{}
	t.Cleanup(imapclient.UseDialer(func(_ context.Context, a config.AccountSpec, password string) (mail.Conn, error) {
		mu.Lock()
		seen[a.Login] = password
		mu.Unlock()
		if passwords[a.Login] != password {
			return nil, fmt.Errorf("AUTHENTICATIONFAILED Invalid credentials")
		}
		return fakes[a.Name].Conn(), nil
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

	c := webG4(t, startServeIn(t, dir))
	add := map[string]any{"id": "work", "label": "Work", "preset": "gmail", "login": "me@work.test", "password": "work-pass"}
	if got := c.do(http.MethodPost, "/web/v1/mail/accounts", add); got.status != http.StatusCreated {
		t.Fatalf("add: %d %.300s", got.status, got.body)
	}

	app := newAppWith(f.lookup, imapclient.Opener, nil, quietLog)
	var out, errs bytes.Buffer
	if code := app.Run(context.Background(), []string{"sync"}, &out, &errs); code != 0 {
		t.Fatalf("sync: %d %s", code, errs.String())
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
	defer func() { _ = d.Close() }()
	for account, want := range map[string]int{"home": 1, "work": 1} {
		var n int
		if err := d.Read.QueryRow("SELECT COUNT(*) FROM messages WHERE account = ?", account).Scan(&n); err != nil || n != want {
			t.Errorf("%s synced %d messages, want %d: %v", account, n, want, err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if seen["me@icloud.test"] != "home-pass" || seen["me@work.test"] != "work-pass" {
		t.Errorf("the dialer saw %v", seen)
	}
}

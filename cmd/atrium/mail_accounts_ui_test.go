package main

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jeiang/atrium/internal/apps/mail"
	"github.com/jeiang/atrium/internal/apps/mail/imapclient"
	"github.com/jeiang/atrium/internal/apps/mail/mailtest"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/connections"
	"github.com/jeiang/atrium/internal/testutil/embedstub"
	"github.com/jeiang/atrium/internal/testutil/mailscript"
)

func TestMailAccountsAddedInTheUITestSyncAndRemoveWithoutLosingMail(t *testing.T) {
	setEnv(t, "ATRIUM_DB_KEY", rightKeyHex)
	dir, api, f := moduleFixture(t, flagsExcept("", ""))
	api.AnswerJSON(mailscript.Load(t).JSONAnswer)
	embed := embedstub.Server(t)
	setEnv(t, "ATRIUM_HOST_CONFIG", accountsFileEmbedding(t, dir, embed.URL+"/v1", filepath.Join(testRoot, "host", "anthropic.key"), mailAccount{"home", "icloud", "me@icloud.test", "home-pass", false}))
	seedSettings(t, map[string]string{mail.KeyMode: "shadow"})

	work := mailtest.NewFakeClient()
	work.Gmail = true
	all := work.AddBox("[Gmail]/All Mail", mailtest.NewBox(9), `\All`)
	work.AddBox("[Gmail]/Trash", mailtest.NewBox(10), `\Trash`)
	h, x := mailtest.RawWith("Standup", "lead@work.test", "<work-1@work.test>", "Moved to 10.")
	all.Add(h, x, nil, []string{`\Inbox`})
	passwords := map[string]string{"me@icloud.test": "home-pass", "me@work.test": "work-pass"}
	var seenMu sync.Mutex
	lastPassword := ""
	t.Cleanup(imapclient.UseDialer(func(_ context.Context, a config.AccountSpec, password string) (mail.Conn, error) {
		seenMu.Lock()
		lastPassword = password
		want := passwords[a.Login]
		seenMu.Unlock()
		if want != password {
			return nil, errors.New("AUTHENTICATIONFAILED Invalid credentials")
		}
		return mailtest.NewFakeClient().Conn(), nil
	}))

	ca := smtpCert(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	smtpServer := &smtpFake{}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go smtpServer.handle(c)
		}
	}()
	t.Setenv("ATRIUM_SMTP_ADDR", ln.Addr().String())
	t.Setenv("ATRIUM_SMTP_CA", ca)

	c := webG4(t, startServeIn(t, dir))
	refused := func(got apiReply) bool {
		return got.status >= http.StatusBadRequest && got.status < http.StatusInternalServerError
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
	count := func(query string, args ...any) int {
		t.Helper()
		var n int
		if err := d.Read.QueryRow(query, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	add := map[string]any{"id": "work", "label": "Work", "preset": "gmail", "login": "me@work.test", "password": "wrong-pass"}
	if got := c.do(http.MethodPost, "/web/v1/mail/accounts", add); got.status != http.StatusConflict || !strings.Contains(got.body, "AUTHENTICATIONFAILED") {
		t.Fatalf("a failed Test: %d %s", got.status, got.body)
	}
	if n := count("SELECT COUNT(*) FROM mail_accounts WHERE pinned = 0"); n != 0 {
		t.Fatalf("a failed Test stored %d accounts", n)
	}
	add["password"] = "work-pass"
	added := c.do(http.MethodPost, "/web/v1/mail/accounts", add)
	if added.status != http.StatusCreated || dataG4(added)["id"] != "work" {
		t.Fatalf("add: %d %s", added.status, added.body)
	}
	listed := c.do(http.MethodGet, "/web/v1/mail/accounts", nil)
	if strings.Contains(added.body, "work-pass") || listed.status != http.StatusOK || strings.Contains(listed.body, "work-pass") {
		t.Error("a response carries the app password")
	}
	if count("SELECT COUNT(*) FROM connection_credentials WHERE connection_id = 'mail/work' AND slot = 'mail/work/password' AND source = 'database'") != 1 ||
		connectionState(t, dir, "mail/work") != string(connections.OK) {
		t.Error("the added account has no stored password or no ok health")
	}
	seenMu.Lock()
	passwords["me@work2.test"] = "work-pass"
	seenMu.Unlock()
	relogin := map[string]any{"label": "Work", "login": "me@work2.test", "enabled": true, "auth_method": "password"}
	if got := c.do(http.MethodPatch, "/web/v1/mail/accounts/work", relogin); got.status != http.StatusOK {
		t.Fatalf("a login change with the stored password: %d %.300s", got.status, got.body)
	}
	if n := count("SELECT COUNT(*) FROM mail_accounts WHERE id = 'work' AND login = 'me@work2.test'"); n != 1 {
		t.Fatal("the login change was not saved")
	}
	seenMu.Lock()
	used := lastPassword
	seenMu.Unlock()
	if used != "work-pass" {
		t.Fatalf("the candidate test used password %q", used)
	}
	update := map[string]any{"label": "Work", "login": "me@work.test", "enabled": true, "auth_method": "xoauth2"}
	if got := c.do(http.MethodPatch, "/web/v1/mail/accounts/work", update); !refused(got) {
		t.Fatalf("switching to xoauth2 without a Google grant: %d %s", got.status, got.body)
	}
	if n := count("SELECT COUNT(*) FROM mail_accounts WHERE id = 'work' AND auth_method = 'password'"); n != 1 {
		t.Fatal("a failed xoauth2 Test changed the stored auth method")
	}
	update["auth_method"] = "password"
	if got := c.do(http.MethodPatch, "/web/v1/mail/accounts/work", update); got.status != http.StatusOK {
		t.Fatalf("saving the password method: %d %s", got.status, got.body)
	}
	if got := c.do(http.MethodPost, "/web/v1/mail/accounts", add); !refused(got) {
		t.Errorf("a duplicate id: %d %s", got.status, got.body)
	}
	if got := c.do(http.MethodDelete, "/web/v1/mail/accounts/home", nil); !refused(got) {
		t.Errorf("removing a deployment account: %d %s", got.status, got.body)
	}
	if count("SELECT COUNT(*) FROM connections WHERE id = 'mail/home'") != 1 {
		t.Error("removing a deployment account dropped its connection")
	}

	seenMu.Lock()
	passwords["me@icloud.test"] = "rotated"
	seenMu.Unlock()
	if got := c.do(http.MethodPost, "/web/v1/mail/accounts/home/test", nil); got.status != http.StatusOK || dataG4(got)["ok"] != false || connectionState(t, dir, "mail/home") != "needs_reauth" {
		t.Errorf("a failing Test of the deployment account: %d %s, state %q", got.status, got.body, connectionState(t, dir, "mail/home"))
	}

	fakes := map[string]*mailtest.FakeClient{"work": work, "home": mailtest.NewFakeClient()}
	fakes["home"].AddBox("INBOX", mailtest.NewBox(3))
	clients := func(config.Config) mail.OpenFunc {
		return func(ctx context.Context, opts mail.OpenOptions) (mail.Client, error) {
			return fakes[opts.Account].Open(ctx, opts)
		}
	}
	app := newAppWith(f.lookup, clients, nil, quietLog)
	var out, errs bytes.Buffer
	if code := app.Run(context.Background(), []string{"sync", "--account", "work"}, &out, &errs); code != 0 {
		t.Fatalf("sync of the added account: %d %s", code, errs.String())
	}
	if n := count("SELECT COUNT(*) FROM messages WHERE account = 'work'"); n != 1 || len(work.Writes) != 0 || len(work.Appended) != 0 {
		t.Errorf("sync of the added account: %d messages, writes %v %v", n, work.Writes, work.Appended)
	}

	if got := c.do(http.MethodDelete, "/web/v1/mail/accounts/work", nil); got.status != http.StatusOK {
		t.Fatalf("remove: %d %s", got.status, got.body)
	}
	if count("SELECT COUNT(*) FROM mail_accounts WHERE pinned = 0") != 0 || count("SELECT COUNT(*) FROM connections WHERE id = 'mail/work'") != 0 {
		t.Error("remove left the account or its connection")
	}
	if n := count("SELECT COUNT(*) FROM messages WHERE account = 'work'"); n != 1 {
		t.Errorf("remove dropped the cached mail: %d messages left", n)
	}
}

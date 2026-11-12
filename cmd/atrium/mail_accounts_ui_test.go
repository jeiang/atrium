//go:build !modernc

package main

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jeiang/atrium/internal/settings"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/connections"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/imapclient"
	"github.com/jeiang/atrium/internal/mail/mailtest"
	"github.com/jeiang/atrium/internal/testutil/llmstub"
)

func TestMailAccountsAddedInTheUITestSyncAndRemoveWithoutLosingMail(t *testing.T) {
	setEnv(t, "ATRIUM_DB_KEY", rightKeyHex)
	t.Cleanup(func() { db.SetDefaultKey(db.Key{}) })
	dir, _, f := moduleFixture(t, flagsExcept("", ""))
	srv := llmstub.Server(t, nil)
	setEnv(t, "ATRIUM_HOST_CONFIG", accountsFileEmbedding(t, dir, srv.URL+"/v1", filepath.Join(testRoot, "host", "anthropic.key"), mailAccount{"home", "icloud", "me@icloud.test", "home-pass", false}))
	setEnv(t, "ATRIUM_MAIL_MAIL_CHAT_URL", srv.URL+"/v1")
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
		seenMu.Unlock()
		if passwords[a.Login] != password {
			return nil, errors.New("AUTHENTICATIONFAILED Invalid credentials")
		}
		return mailtest.NewFakeClient(), nil
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

	base := startServeIn(t, dir)
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	page := func() string {
		t.Helper()
		code, body, _ := fetch(t, client, http.MethodGet, base+"/settings/mail", nil)
		if code != http.StatusOK {
			t.Fatalf("mail settings: %d", code)
		}
		return body
	}
	post := func(path string, form url.Values) (int, string) {
		t.Helper()
		page()
		if form == nil {
			form = url.Values{}
		}
		code, body, _ := fetch(t, client, http.MethodPost, base+"/settings/mail"+path, form)
		return code, body
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
	count := func(query string, args ...any) int {
		t.Helper()
		var n int
		if err := d.Read.QueryRow(query, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	first := page()
	if !strings.Contains(first, `data-account="home"`) || strings.Contains(first, `id="login-home"`) || !strings.Contains(first, "set by deployment") {
		t.Error("the deployment account is not shown read-only")
	}
	if strings.Contains(first, "mail_mode") || strings.Contains(first, `name="mode"`) {
		t.Error("the accounts page offers a live or shadow control")
	}

	add := url.Values{"id": {"work"}, "label": {"Work"}, "preset": {"gmail"}, "login": {"me@work.test"}, "password": {"wrong-pass"}}
	if code, body := post("/accounts", add); code != http.StatusUnprocessableEntity || !strings.Contains(body, "AUTHENTICATIONFAILED") {
		t.Fatalf("a failed Test: %d", code)
	}
	if n := count("SELECT COUNT(*) FROM mail_accounts WHERE pinned = 0"); n != 0 {
		t.Fatalf("a failed Test stored %d accounts", n)
	}
	add.Set("password", "work-pass")
	code, body := post("/accounts", add)
	if code != http.StatusOK || !strings.Contains(body, `data-account="work" data-state="ok"`) {
		t.Fatalf("add: %d", code)
	}
	if strings.Contains(body, "work-pass") || strings.Contains(page(), "work-pass") {
		t.Error("a page response carries the app password")
	}
	if count("SELECT COUNT(*) FROM connection_credentials WHERE connection_id = 'mail/work' AND slot = 'mail/work/password' AND source = 'database'") != 1 ||
		connectionState(t, dir, "mail/work") != string(connections.OK) {
		t.Error("the added account has no stored password or no ok health")
	}
	if !strings.Contains(page(), `id="new-auth" name="auth_method"`) {
		t.Error("the add form does not offer an auth method")
	}
	passwords["me@work2.test"] = "work-pass"
	relogin := url.Values{"label": {"Work"}, "login": {"me@work2.test"}, "enabled": {"1"}, "auth_method": {"password"}}
	if code, body := post("/accounts/work", relogin); code != http.StatusOK {
		t.Fatalf("a login change with the stored password: %d %.300s", code, body)
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
	update := url.Values{"label": {"Work"}, "login": {"me@work.test"}, "enabled": {"1"}, "auth_method": {"xoauth2"}}
	if code, _ := post("/accounts/work", update); code != http.StatusUnprocessableEntity {
		t.Fatalf("switching to xoauth2 without a Google grant: %d", code)
	}
	if n := count("SELECT COUNT(*) FROM mail_accounts WHERE id = 'work' AND auth_method = 'password'"); n != 1 {
		t.Fatal("a failed xoauth2 Test changed the stored auth method")
	}
	update.Set("auth_method", "password")
	if code, _ := post("/accounts/work", update); code != http.StatusOK {
		t.Fatalf("saving the password method: %d", code)
	}
	if code, _ := post("/accounts", add); code != http.StatusUnprocessableEntity {
		t.Errorf("a duplicate id: %d", code)
	}
	if code, _ := post("/accounts/home/remove", nil); code != http.StatusUnprocessableEntity {
		t.Errorf("removing a deployment account: %d", code)
	}

	passwords["me@icloud.test"] = "rotated"
	if code, body := post("/accounts/home/test", nil); code != http.StatusUnprocessableEntity || !strings.Contains(body, `data-account="home" data-state="needs_reauth"`) {
		t.Errorf("a failing Test of the deployment account: %d", code)
	}

	fakes := map[string]*mailtest.FakeClient{"work": work, "home": mailtest.NewFakeClient()}
	fakes["home"].AddBox("INBOX", mailtest.NewBox(3))
	clients := func(config.Config) mail.OpenFunc {
		return func(_ context.Context, opts mail.OpenOptions) (mail.Client, error) {
			return mail.Gated(fakes[opts.Account], opts.Q), nil
		}
	}
	reg := settings.New()
	app := newAppWith(f.lookup, reg, config.NewState(reg), nil, clients, nil, quietLog)
	var out, errs bytes.Buffer
	if code := app.Run(context.Background(), []string{"sync", "--account", "work"}, &out, &errs); code != 0 {
		t.Fatalf("sync of the added account: %d %s", code, errs.String())
	}
	if n := count("SELECT COUNT(*) FROM messages WHERE account = 'work'"); n != 1 || len(work.Writes) != 0 || len(work.Appended) != 0 {
		t.Errorf("sync of the added account: %d messages, writes %v %v", n, work.Writes, work.Appended)
	}

	if code, _ := post("/accounts/work/remove", nil); code != http.StatusOK {
		t.Fatalf("remove: %d", code)
	}
	if count("SELECT COUNT(*) FROM mail_accounts WHERE pinned = 0") != 0 || count("SELECT COUNT(*) FROM connections WHERE id = 'mail/work'") != 0 {
		t.Error("remove left the account or its connection")
	}
	if n := count("SELECT COUNT(*) FROM messages WHERE account = 'work'"); n != 1 {
		t.Errorf("remove dropped the cached mail: %d messages left", n)
	}
}

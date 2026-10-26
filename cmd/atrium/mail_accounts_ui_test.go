//go:build !modernc

package main

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/integrations"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/imapclient"
	"github.com/jeiang/atrium/internal/mail/mailtest"
	"github.com/jeiang/atrium/internal/testutil/llmstub"
)

func TestMailAccountsAddedInTheUITestSyncAndRemoveWithoutLosingMail(t *testing.T) {
	dir, _ := moduleFixture(t, flagsExcept("", ""))
	srv := llmstub.Server(t, nil)
	t.Setenv("ATRIUM_DB_KEY", rightKeyHex)
	t.Cleanup(func() { db.SetDefaultKey(db.Key{}) })
	t.Setenv(settingEnv(config.KeyMailAccountsFile), accountsFile(t, dir, mailAccount{"home", "icloud", "me@icloud.test", "home-pass", false}))
	t.Setenv("ATRIUM_MAIL_MAIL_CHAT_URL", srv.URL+"/v1")
	t.Setenv("ATRIUM_MAIL_MAIL_EMBED_URL", srv.URL+"/v1")
	t.Setenv("ATRIUM_MAIL_MAIL_MODE", "shadow")

	work := mailtest.NewFakeClient()
	work.Gmail = true
	all := work.AddBox("[Gmail]/All Mail", mailtest.NewBox(9), `\All`)
	work.AddBox("[Gmail]/Trash", mailtest.NewBox(10), `\Trash`)
	h, x := mailtest.RawWith("Standup", "lead@work.test", "<work-1@work.test>", "Moved to 10.")
	all.Add(h, x, nil, []string{`\Inbox`})
	passwords := map[string]string{"me@icloud.test": "home-pass", "me@work.test": "work-pass"}
	t.Cleanup(imapclient.UseDialer(func(_ context.Context, a config.AccountSpec) (mail.Conn, error) {
		if passwords[a.Login] != a.Password {
			return nil, errors.New("AUTHENTICATIONFAILED Invalid credentials")
		}
		return mailtest.NewFakeClient(), nil
	}))

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
	if n := count("SELECT COUNT(*) FROM mail_accounts"); n != 0 {
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
	if count("SELECT COUNT(*) FROM secrets WHERE name = ?", config.PasswordKey("work")) != 1 || health(t, dir, "mail_work") != integrations.OK {
		t.Error("the added account has no stored password or no ok health")
	}
	if code, _ := post("/accounts", add); code != http.StatusUnprocessableEntity {
		t.Errorf("a duplicate id: %d", code)
	}
	if code, _ := post("/accounts/home/remove", nil); code != http.StatusUnprocessableEntity {
		t.Errorf("removing a deployment account: %d", code)
	}

	passwords["me@icloud.test"] = "rotated"
	if code, body := post("/accounts/home/test", nil); code != http.StatusUnprocessableEntity || !strings.Contains(body, `data-account="home" data-state="failing"`) {
		t.Errorf("a failing Test of the deployment account: %d", code)
	}

	fakes := map[string]*mailtest.FakeClient{"work": work, "home": mailtest.NewFakeClient()}
	fakes["home"].AddBox("INBOX", mailtest.NewBox(3))
	clients := func(config.Config) mail.OpenFunc {
		return func(_ context.Context, account string) (mail.Client, error) { return fakes[account], nil }
	}
	app := cli.New("atrium", logx.Setup)
	app.Add(commands(clients, nil)...)
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
	if count("SELECT COUNT(*) FROM mail_accounts") != 0 || count("SELECT COUNT(*) FROM secrets WHERE name = ?", config.PasswordKey("work")) != 0 {
		t.Error("remove left the account or its password")
	}
	if n := count("SELECT COUNT(*) FROM messages WHERE account = 'work'"); n != 1 {
		t.Errorf("remove dropped the cached mail: %d messages left", n)
	}
}

package main

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jeiang/atrium/internal/settings"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/imapclient"
	"github.com/jeiang/atrium/internal/mail/mailtest"
	"github.com/jeiang/atrium/internal/testutil/llmstub"
)

type mailAccount struct {
	id, preset, login, password string
	disabled                    bool
}

func accountsFile(t *testing.T, dir string, accounts ...mailAccount) string {
	t.Helper()
	return accountsFileWith(t, dir, filepath.Join(testRoot, "host", "anthropic.key"), accounts...)
}

func accountsFileWith(t *testing.T, dir, anthropicKey string, accounts ...mailAccount) string {
	t.Helper()
	var b strings.Builder
	b.WriteString(hostConfigBodyWith(defaultWebPushConnection, anthropicKey))
	for _, a := range accounts {
		secret := filepath.Join(dir, a.id+".password")
		if err := os.WriteFile(secret, []byte(a.password+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		b.WriteString(mailConnection(a.id, a.preset, a.login, secret, !a.disabled))
	}
	path := filepath.Join(dir, "host.toml")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestMailAccountsFileDefinesWhichAccountsSyncAndRemovalKeepsMail(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	srv := llmstub.Server(t, nil)
	setEnv(t, "ATRIUM_DB", filepath.Join(dir, "atrium.db"))
	setEnv(t, "ATRIUM_CACHE_DIR", filepath.Join(dir, "cache"))
	setEnv(t, "ATRIUM_MAIL_MAIL_CHAT_URL", srv.URL+"/v1")
	setEnv(t, "ATRIUM_MAIL_MAIL_EMBED_URL", srv.URL+"/v1")
	setEnv(t, "ATRIUM_MAIL_MAIL_MODE", "shadow")

	home := mailtest.NewFakeClient()
	inbox := home.AddBox("INBOX", mailtest.NewBox(7))
	h, x := mailtest.RawWith("Lunch", "friend@example.test", "<home-1@example.test>", "See you at noon.")
	inbox.Add(h, x, nil, nil)
	work := mailtest.NewFakeClient()
	work.Gmail = true
	all := work.AddBox("[Gmail]/All Mail", mailtest.NewBox(9), `\All`)
	work.AddBox("[Gmail]/Trash", mailtest.NewBox(10), `\Trash`)
	h, x = mailtest.RawWith("Standup", "lead@work.test", "<work-1@work.test>", "Moved to 10.")
	all.Add(h, x, nil, []string{`\Inbox`})
	fakes := map[string]*mailtest.FakeClient{"home": home, "work": work}
	var mu sync.Mutex
	opened := map[string]int{}
	clients := func(config.Config) mail.OpenFunc {
		return func(_ context.Context, opts mail.OpenOptions) (mail.Client, error) {
			mu.Lock()
			defer mu.Unlock()
			opened[opts.Account]++
			if f, ok := fakes[opts.Account]; ok {
				return mail.Gated(f, opts.Q), nil
			}
			return nil, fmt.Errorf("unexpected account %q", opts.Account)
		}
	}
	run := func(args ...string) appRun {
		reg := settings.New()
		app := newAppWith(testLookupFor(t), reg, config.NewState(reg), nil, clients, nil, quietLog)
		var out, errs bytes.Buffer
		code := app.Run(context.Background(), args, &out, &errs)
		return appRun{code, out.String(), errs.String()}
	}
	key := "ATRIUM_HOST_CONFIG"
	path := accountsFile(t, dir,
		mailAccount{"home", "icloud", "me@icloud.test", "home pass", false},
		mailAccount{"work", "gmail", "me@work.test", "work-pass", false},
		mailAccount{"off", "gmail", "off@gmail.test", "off-pass", true})
	setEnv(t, key, path)

	counts := func() map[string]int {
		d := mustConnect(t, filepath.Join(dir, "atrium.db"))
		rows, err := d.Read.Query("SELECT m.account || ':' || a.kind, COUNT(*) FROM messages m JOIN accounts a ON a.name = m.account GROUP BY m.account")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = rows.Close() }()
		out := map[string]int{}
		for rows.Next() {
			var k string
			var n int
			if err := rows.Scan(&k, &n); err != nil {
				t.Fatal(err)
			}
			out[k] = n
		}
		return out
	}

	if got := run("sync", "--account", "off"); got.code == 0 || opened["off"] != 0 {
		t.Errorf("sync of a disabled account: %+v, opened %v", got, opened)
	}
	if got := run("sync"); got.code != 0 {
		t.Fatalf("sync: %+v", got)
	}
	if got := counts(); got["home:icloud"] != 1 || got["work:gmail"] != 1 || len(got) != 2 {
		t.Errorf("synced messages by account and preset = %v", got)
	}
	if opened["off"] != 0 || opened["home"] == 0 || opened["work"] == 0 {
		t.Errorf("opened accounts = %v; the disabled account must not be opened", opened)
	}
	for name, f := range fakes {
		if len(f.Writes) != 0 || len(f.Appended) != 0 {
			t.Errorf("%s: sync in shadow mode wrote to the mailbox: %v %v", name, f.Writes, f.Appended)
		}
	}

	setEnv(t, key, accountsFile(t, t.TempDir(), mailAccount{"home", "icloud", "me@icloud.test", "home pass", false}))
	opened = map[string]int{}
	if got := run("sync"); got.code != 0 || opened["work"] != 0 {
		t.Fatalf("sync after removing work: %+v, opened %v", got, opened)
	}
	if got := counts(); got["work:gmail"] != 1 {
		t.Errorf("removing an account dropped its cached mail: %v", got)
	}

	bad := filepath.Join(dir, "bad.toml")
	body := hostConfigBody(defaultWebPushConnection) + "\n[connections.\"mail/x\"]\nkind = \"mail\"\nconfig = { preset = \"yahoo\", login = \"a\", auth_method = \"password\" }\ncapabilities = [\"imap\", \"smtp\"]\ncredential_files = { \"mail/x/password\" = \"" + filepath.Join(dir, "home.password") + "\" }\n"
	if err := os.WriteFile(bad, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	setEnv(t, key, bad)
	if got := run("sync"); got.code == 0 || !strings.Contains(got.stderr, "preset must be icloud or gmail") {
		t.Errorf("an unknown preset: %+v", got)
	}
}

func insertMailAccount(t *testing.T, d *db.DB, id, preset, login, method string) {
	t.Helper()
	cfg := fmt.Sprintf(`{"preset":%q,"login":%q,"auth_method":%q}`, preset, login, method)
	if _, err := d.Write.Exec("INSERT INTO connections (id, kind, label, config_source, config_json, transport_json, revision, enabled, state, created_at, updated_at) VALUES (?, 'mail', ?, 'operator', ?, '{}', 1, 1, 'untested', 0, 0)",
		"mail/"+id, id, cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.Exec("INSERT INTO mail_accounts (id, label, preset, login, enabled, pinned, auth_method, connection_id, capability_prefix, created_at) VALUES (?, ?, ?, ?, 1, 0, ?, ?, ?, 0)",
		id, id, preset, login, method, "mail/"+id, "mail/"+id); err != nil {
		t.Fatal(err)
	}
	for _, capability := range []string{"imap", "smtp"} {
		if _, err := d.Write.Exec("INSERT INTO connection_capabilities (connection_id, name, enabled, config_json) VALUES (?, ?, 1, '{}')", "mail/"+id, capability); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAHostMailConnectionAndAnAccountAddedInTheUIBothSync(t *testing.T) {
	dir, _, f := moduleFixture(t, flagsExcept("", ""))
	setEnv(t, "ATRIUM_DB_KEY", rightKeyHex)
	t.Cleanup(func() { db.SetDefaultKey(db.Key{}) })
	setEnv(t, "ATRIUM_HOST_CONFIG", accountsFile(t, dir, mailAccount{"home", "icloud", "me@icloud.test", "home-pass", false}))
	srv := llmstub.Server(t, nil)
	setEnv(t, "ATRIUM_MAIL_MAIL_CHAT_URL", srv.URL+"/v1")
	setEnv(t, "ATRIUM_MAIL_MAIL_EMBED_URL", srv.URL+"/v1")
	setEnv(t, "ATRIUM_MAIL_MAIL_MODE", "shadow")

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
		return fakes[a.Name], nil
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

	base := startServeIn(t, dir)
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	if code, _, _ := fetch(t, client, http.MethodGet, base+"/settings/mail", nil); code != http.StatusOK {
		t.Fatalf("mail settings: %d", code)
	}
	add := url.Values{"id": {"work"}, "label": {"Work"}, "preset": {"gmail"}, "login": {"me@work.test"}, "password": {"work-pass"}}
	if code, body, _ := fetch(t, client, http.MethodPost, base+"/settings/mail/accounts", add); code != http.StatusOK {
		t.Fatalf("add: %d %.300s", code, body)
	}

	reg := settings.New()
	app := newAppWith(f.lookup, reg, config.NewState(reg), nil, imapclient.Opener, nil, quietLog)
	var out, errs bytes.Buffer
	if code := app.Run(context.Background(), []string{"sync"}, &out, &errs); code != 0 {
		t.Fatalf("sync: %d %s", code, errs.String())
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
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

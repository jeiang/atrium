package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/mailtest"
	"github.com/jeiang/atrium/internal/testutil/llmstub"
)

type mailAccount struct {
	id, preset, login, password string
	disabled                    bool
}

func accountsFile(t *testing.T, dir string, accounts ...mailAccount) string {
	t.Helper()
	var b strings.Builder
	for _, a := range accounts {
		secret := filepath.Join(dir, a.id+".password")
		if err := os.WriteFile(secret, []byte(a.password+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&b, "[[account]]\nid = %q\npreset = %q\nlogin = %q\npassword_file = %q\nenabled = %t\n\n", a.id, a.preset, a.login, secret, !a.disabled)
	}
	path := filepath.Join(dir, "accounts.toml")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestMailAccountsFileDefinesWhichAccountsSyncAndRemovalKeepsMail(t *testing.T) {
	dir := t.TempDir()
	srv := llmstub.Server(t, nil)
	t.Setenv("ATRIUM_DB", filepath.Join(dir, "atrium.db"))
	t.Setenv("ATRIUM_CACHE_DIR", filepath.Join(dir, "cache"))
	t.Setenv("ATRIUM_MAIL_MAIL_CHAT_URL", srv.URL+"/v1")
	t.Setenv("ATRIUM_MAIL_MAIL_EMBED_URL", srv.URL+"/v1")
	t.Setenv("ATRIUM_MAIL_MAIL_MODE", "shadow")

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
		return func(_ context.Context, account string) (mail.Client, error) {
			mu.Lock()
			defer mu.Unlock()
			opened[account]++
			if f, ok := fakes[account]; ok {
				return f, nil
			}
			return nil, fmt.Errorf("unexpected account %q", account)
		}
	}
	run := func(args ...string) appRun {
		app := cli.New("atrium", logx.Setup)
		app.Add(commands(clients, nil)...)
		var out, errs bytes.Buffer
		code := app.Run(context.Background(), args, &out, &errs)
		return appRun{code, out.String(), errs.String()}
	}
	key := settingEnv(config.KeyMailAccountsFile)
	path := accountsFile(t, dir,
		mailAccount{"home", "icloud", "me@icloud.test", "home pass", false},
		mailAccount{"work", "gmail", "me@work.test", "work-pass", false},
		mailAccount{"off", "gmail", "off@gmail.test", "off-pass", true})
	t.Setenv(key, path)

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

	t.Setenv(key, accountsFile(t, t.TempDir(), mailAccount{"home", "icloud", "me@icloud.test", "home pass", false}))
	opened = map[string]int{}
	if got := run("sync"); got.code != 0 || opened["work"] != 0 {
		t.Fatalf("sync after removing work: %+v, opened %v", got, opened)
	}
	if got := counts(); got["work:gmail"] != 1 {
		t.Errorf("removing an account dropped its cached mail: %v", got)
	}

	bad := filepath.Join(dir, "bad.toml")
	if err := os.WriteFile(bad, []byte("[[account]]\nid = \"x\"\npreset = \"yahoo\"\nlogin = \"a\"\npassword_file = \""+filepath.Join(dir, "home.password")+"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(key, bad)
	if got := run("sync"); got.code == 0 || !strings.Contains(got.stderr, "preset must be icloud or gmail") {
		t.Errorf("an unknown preset: %+v", got)
	}
}

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

	"github.com/jeiang/atrium/internal/settings"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/mailtest"
	"github.com/jeiang/atrium/internal/module"
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
	return accountsFileEmbedding(t, dir, "", anthropicKey, accounts...)
}

func accountsFileEmbedding(t *testing.T, dir, embedURL, anthropicKey string, accounts ...mailAccount) string {
	t.Helper()
	embedding := ""
	if embedURL != "" {
		embedding = embeddingTable(embedURL)
	}
	var b strings.Builder
	b.WriteString(hostConfigBodyWith(embedding, defaultWebPushConnection, anthropicKey))
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
	seedSettings(t, map[string]string{mail.KeyMode: "shadow", module.FlagKey(module.Mail): "true"})

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
		app := newAppWith(testLookupFor(t), reg, config.NewState(reg), clients, nil, quietLog)
		var out, errs bytes.Buffer
		code := app.Run(context.Background(), args, &out, &errs)
		return appRun{code, out.String(), errs.String()}
	}
	key := "ATRIUM_HOST_CONFIG"
	path := accountsFileEmbedding(t, dir, srv.URL+"/v1", filepath.Join(testRoot, "host", "anthropic.key"),
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

	setEnv(t, key, accountsFileEmbedding(t, t.TempDir(), srv.URL+"/v1", filepath.Join(testRoot, "host", "anthropic.key"), mailAccount{"home", "icloud", "me@icloud.test", "home pass", false}))
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

func embeddingTable(url string) string {
	return fmt.Sprintf(`[connections."service/embedding"]
kind = "service/embedding"
label = "Embedding"
config = { url = %q, model = "embeddinggemma" }
capabilities = ["embeddings"]

`, url)
}

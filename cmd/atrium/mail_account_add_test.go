//go:build !modernc

package main

import (
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"path/filepath"
	"testing"

	"github.com/jeiang/atrium/internal/mail/mailtest"
)

func TestMailAccountAddLeavesNothingWhenTheTestFails(t *testing.T) {
	dir, embed := keyedMailFixture(t)
	setEnv(t, "ATRIUM_HOST_CONFIG", accountsFileEmbedding(t, dir, embed, filepath.Join(testRoot, "host", "anthropic.key"), mailAccount{"home", "icloud", "me@home.test", "home-pass", false}))
	fake := mailtest.NewFakeClient()
	fake.AddBox("INBOX", mailtest.NewBox(1))
	strictIMAPRig(t, map[string]string{"me@work.test": "work-pass"}, map[string]*mailtest.FakeClient{"work": fake})
	strictSMTPRig(t, map[string]string{"me@work.test": "work-pass"})

	base := startServeIn(t, dir)
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	if code, _, _ := fetch(t, client, http.MethodGet, base+"/settings/mail", nil); code != http.StatusOK {
		t.Fatalf("mail settings: %d", code)
	}
	add := func(id, password string) (int, string) {
		form := url.Values{"id": {id}, "label": {id}, "preset": {"icloud"}, "login": {"me@work.test"}, "password": {password}, "auth_method": {"password"}}
		code, body, _ := fetch(t, client, http.MethodPost, base+"/settings/mail/accounts", form)
		return code, body
	}
	if code, body := add("work", "wrong-pass"); code != http.StatusUnprocessableEntity {
		t.Fatalf("add with a rejected password = %d: %.300s", code, body)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
	for _, query := range []string{
		"SELECT COUNT(*) FROM mail_accounts WHERE id = 'work'",
		"SELECT COUNT(*) FROM connections WHERE id = 'mail/work'",
		"SELECT COUNT(*) FROM connection_credentials WHERE connection_id = 'mail/work'",
	} {
		if n := mailCount(t, d, query); n != 0 {
			t.Fatalf("a failed add left %d rows for %s", n, query)
		}
	}

	if _, err := d.Write.Exec("INSERT INTO connections (id, kind, label, config_source, config_json, transport_json, revision, enabled, state, created_at, updated_at) " +
		"VALUES ('mail/stale', 'mail', 'stale', 'operator', '{}', '{}', 1, 1, 'untested', 0, 0)"); err != nil {
		t.Fatal(err)
	}
	if code, body := add("stale", "wrong-pass"); code != http.StatusUnprocessableEntity {
		t.Fatalf("add over a leftover connection with a rejected password = %d: %.300s", code, body)
	}
	if n := mailCount(t, d, "SELECT COUNT(*) FROM connections WHERE id = 'mail/stale'"); n != 0 {
		t.Fatalf("a failed add left %d rows for a connection from an earlier attempt", n)
	}

	if code, body := add("work", "work-pass"); code != http.StatusOK {
		t.Fatalf("add with the accepted password = %d: %.300s", code, body)
	}
	for _, query := range []string{
		"SELECT COUNT(*) FROM mail_accounts WHERE id = 'work'",
		"SELECT COUNT(*) FROM connections WHERE id = 'mail/work'",
		"SELECT COUNT(*) FROM connection_credentials WHERE connection_id = 'mail/work' AND slot = 'mail/work/password'",
	} {
		if n := mailCount(t, d, query); n != 1 {
			t.Fatalf("a successful add left %d rows for %s", n, query)
		}
	}

	if code, body := add("home", "work-pass"); code != http.StatusUnprocessableEntity {
		t.Fatalf("add of an existing account id = %d: %.300s", code, body)
	}
	for _, query := range []string{
		"SELECT COUNT(*) FROM mail_accounts WHERE id = 'home'",
		"SELECT COUNT(*) FROM connections WHERE id = 'mail/home'",
	} {
		if n := mailCount(t, d, query); n != 1 {
			t.Fatalf("a refused add left %d rows for the existing %s", n, query)
		}
	}
}

package main

import (
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/mail/mailtest"
)

func TestMailAccountUpdateTestsTheSubmittedLogin(t *testing.T) {
	dir := keyedMailFixture(t)
	setEnv(t, "ATRIUM_HOST_CONFIG", accountsFile(t, dir))
	fake := mailtest.NewFakeClient()
	fake.AddBox("INBOX", mailtest.NewBox(1))
	creds := map[string]string{"me@work.test": "work-pass", "me@other.test": "other-pass"}
	strictIMAPRig(t, creds, map[string]*mailtest.FakeClient{"work": fake})
	strictSMTPRig(t, creds)

	base := startServeIn(t, dir)
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	if code, _, _ := fetch(t, client, http.MethodGet, base+"/settings/mail", nil); code != http.StatusOK {
		t.Fatalf("mail settings: %d", code)
	}
	add := url.Values{"id": {"work"}, "label": {"Work"}, "preset": {"icloud"}, "login": {"me@work.test"}, "password": {"work-pass"}, "auth_method": {"password"}}
	if code, body, _ := fetch(t, client, http.MethodPost, base+"/settings/mail/accounts", add); code != http.StatusOK {
		t.Fatalf("add: %d %.300s", code, body)
	}
	update := func(login, password string) (int, string) {
		form := url.Values{"label": {"Work"}, "login": {login}, "password": {password}, "enabled": {"on"}, "auth_method": {"password"}}
		code, body, _ := fetch(t, client, http.MethodPost, base+"/settings/mail/accounts/work", form)
		return code, body
	}
	if code, body := update("me@other.test", "work-pass"); code != http.StatusUnprocessableEntity {
		t.Fatalf("update to a login the password cannot authenticate = %d: %.300s", code, body)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
	var login, configJSON string
	if err := d.Read.QueryRow("SELECT login FROM mail_accounts WHERE id = 'work'").Scan(&login); err != nil {
		t.Fatal(err)
	}
	if login != "me@work.test" {
		t.Fatalf("the account login after a failed update is %q, want the tested one", login)
	}
	if err := d.Read.QueryRow("SELECT config_json FROM connections WHERE id = 'mail/work'").Scan(&configJSON); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(configJSON, `"login":"me@work.test"`) {
		t.Fatalf("the connection config after a failed update is %s", configJSON)
	}

	if code, body := update("me@other.test", "other-pass"); code != http.StatusOK {
		t.Fatalf("update to a login its password authenticates = %d: %.300s", code, body)
	}
	if err := d.Read.QueryRow("SELECT login FROM mail_accounts WHERE id = 'work'").Scan(&login); err != nil {
		t.Fatal(err)
	}
	if login != "me@other.test" {
		t.Fatalf("the account login after a passing update is %q", login)
	}
	if err := d.Read.QueryRow("SELECT config_json FROM connections WHERE id = 'mail/work'").Scan(&configJSON); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(configJSON, `"login":"me@other.test"`) {
		t.Fatalf("the connection config after a passing update is %s", configJSON)
	}
}

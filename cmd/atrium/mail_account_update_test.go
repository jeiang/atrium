package main

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/apps/mail/mailtest"
)

func TestMailAccountUpdateTestsTheSubmittedLogin(t *testing.T) {
	dir, embed := keyedMailFixture(t)
	setEnv(t, "ATRIUM_HOST_CONFIG", accountsFileEmbedding(t, dir, embed, filepath.Join(testRoot, "host", "anthropic.key")))
	fake := mailtest.NewFakeClient()
	fake.AddBox("INBOX", mailtest.NewBox(1))
	creds := map[string]string{"me@work.test": "work-pass", "me@other.test": "other-pass"}
	strictIMAPRig(t, creds, map[string]*mailtest.FakeClient{"work": fake})
	strictSMTPRig(t, creds)

	base := startServeIn(t, dir)
	client := localWebG5(t, base)
	add := map[string]any{"id": "work", "label": "Work", "preset": "icloud", "login": "me@work.test", "password": "work-pass", "auth_method": "password"}
	if got := client.do(http.MethodPost, "/web/v1/mail/accounts", add); got.status != http.StatusCreated {
		t.Fatalf("add: %d %.300s", got.status, got.body)
	}
	update := func(login, password string) (int, string) {
		got := client.do(http.MethodPatch, "/web/v1/mail/accounts/work", map[string]any{"label": "Work", "login": login, "password": password, "enabled": true, "auth_method": "password"})
		return got.status, got.body
	}
	if code, body := update("me@other.test", "work-pass"); code != http.StatusConflict || !strings.Contains(body, `"refused"`) {
		t.Fatalf("update to a login the password cannot authenticate = %d: %.300s", code, body)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
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
	empty := map[string]any{"label": "", "login": "", "password": "", "enabled": true}
	if got := client.do(http.MethodPatch, "/web/v1/mail/accounts/work", empty); got.status != http.StatusOK {
		t.Fatalf("empty fields update = %d: %.300s", got.status, got.body)
	}
	var label, authMethod string
	if err := d.Read.QueryRow("SELECT label, login, auth_method FROM mail_accounts WHERE id = 'work'").Scan(&label, &login, &authMethod); err != nil {
		t.Fatal(err)
	}
	if label != "Work" || login != "me@other.test" || authMethod != "password" {
		t.Fatalf("empty fields changed account values: label=%q login=%q auth=%q", label, login, authMethod)
	}
	var added, saved, refused int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM operation_audit WHERE operation = 'mail.account-add' AND outcome = 'ok' AND source = 'api' AND actor != '' AND target = 'work'").Scan(&added); err != nil {
		t.Fatal(err)
	}
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM operation_audit WHERE operation = 'mail.account-update' AND outcome = 'ok' AND source = 'api' AND actor != '' AND target = 'work'").Scan(&saved); err != nil {
		t.Fatal(err)
	}
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM operation_audit WHERE operation = 'mail.account-update' AND outcome = 'refused' AND source = 'api' AND actor != '' AND target = 'work'").Scan(&refused); err != nil {
		t.Fatal(err)
	}
	if added != 1 || saved != 2 || refused != 1 {
		t.Fatalf("account operation audits: added=%d saved=%d refused=%d", added, saved, refused)
	}
}

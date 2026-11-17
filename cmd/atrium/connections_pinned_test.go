package main

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/apps/calendar"
	"github.com/jeiang/atrium/internal/apps/calendar/caldavtest"
	"github.com/jeiang/atrium/internal/contacts"
	"github.com/jeiang/atrium/internal/davmirror"
)

func TestAPinnedCredentialIsRefusedOnEverySurfaceAndTheSharedSlotNamesItsOwner(t *testing.T) {
	requireCipher(t)
	setEnv(t, "ATRIUM_DB_KEY", rightKeyHex)
	dir, _, f := moduleFixture(t, flagsExcept("", ""))
	srv := caldavtest.New(t, "operator@icloud.invalid", davPassword, "Home")
	setEnv(t, davmirror.CalDAVEnv, srv.URL+"/")
	setEnv(t, davmirror.CardDAVEnv, srv.URL+"/")
	setEnv(t, settingEnv(contacts.DirKey), filepath.Join(dir, "contacts"))
	setEnv(t, settingEnv(calendar.CalendarDirKey), filepath.Join(dir, "calendars"))
	seedSettings(t, map[string]string{davmirror.AppleIDKey: "operator@icloud.invalid"})
	host := accountsFile(t, dir, mailAccount{"icloud", "icloud", "operator@icloud.invalid", "wrong", false})
	file, err := os.OpenFile(host, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprint(file, "\n[connections.\"icloud/icloud\"]\nkind = \"icloud\"\nlabel = \"iCloud DAV\"\ncapabilities = [\"caldav\", \"carddav\"]\n"); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	setEnv(t, "ATRIUM_HOST_CONFIG", host)
	t.Setenv(davmirror.CalDAVEnv, srv.URL+"/")
	t.Setenv(davmirror.CardDAVEnv, srv.URL+"/")

	if view := testICloudConnection(t); view.State != "needs_reauth" {
		t.Fatalf("the iCloud connection with a wrong pinned password = %+v, want needs_reauth", view)
	}

	token := localAPIToken(t, "pinned")
	base, _, stop, err := startServeAppArgs(t, f.app, &lockedBuffer{}, "--auth", "local")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	s := &apiSite{t: t, base: base}
	d := keyedDB(t, filepath.Join(dir, "atrium.db"))
	row := func() string {
		return queryValue(t, d.Read, "SELECT source || ':' || COALESCE(value, '') || ':' || set_at FROM connection_credentials WHERE connection_id = 'mail/icloud' AND slot = 'mail/icloud/password'")
	}
	audits := func() string { return queryValue(t, d.Read, "SELECT COUNT(*) FROM settings_audit") }
	before, audited := row(), audits()
	if !strings.HasPrefix(before, "host_file::") {
		t.Fatalf("the pinned mail password row = %q, want a host file", before)
	}

	var views map[string]map[string]any
	for _, c := range apiData(t, s.do(http.MethodGet, "/api/v1/connections", token, nil), http.StatusOK)["connections"].([]any) {
		c := c.(map[string]any)
		if views == nil {
			views = map[string]map[string]any{}
		}
		if creds, _ := c["credentials"].([]any); len(creds) > 0 {
			views[c["id"].(string)] = creds[0].(map[string]any)
		}
	}
	if mail := views["mail/icloud"]; mail["pinned"] != true || mail["owner"] != nil {
		t.Fatalf("mail/icloud credential view = %v, want pinned", mail)
	}
	if shared := views["icloud/icloud"]; shared["owner"] != "mail/icloud" || shared["pinned"] != true || shared["file_ref"] != nil {
		t.Fatalf("icloud/icloud credential view = %v, want the owner, pinned and no file path", shared)
	}

	body := map[string]any{"credentials": map[string]string{"mail/icloud/password": "ui-value"}}
	expectAPI(t, s.do(http.MethodPatch, "/api/v1/connections/mail/icloud", token, body), http.StatusConflict, "refused")
	expectAPI(t, s.do(http.MethodPut, "/api/v1/connections/mail/icloud", token, body), http.StatusConflict, "refused")
	expectAPI(t, s.do(http.MethodPatch, "/api/v1/connections/icloud/icloud", token, body), http.StatusConflict, "refused")
	browser := newWebClient(t, base, nil)
	browser.session("local")
	expectAPI(t, browser.do(http.MethodPatch, "/web/v1/connections/mail/icloud", body), http.StatusConflict, "refused")
	refused := browser.do(http.MethodPut, "/web/v1/connections/mail/icloud", body)
	expectAPI(t, refused, http.StatusConflict, "refused")
	if !strings.Contains(refused.body, "pinned by host configuration") || strings.Contains(refused.body, "ui-value") {
		t.Fatalf("web save over a pinned slot: %s", refused.body)
	}
	listed := browser.do(http.MethodGet, "/web/v1/connections", nil)
	if strings.Contains(listed.body, "ui-value") {
		t.Fatalf("web connections list: %s", listed.body)
	}
	for _, c := range apiData(t, listed, http.StatusOK)["connections"].([]any) {
		c := c.(map[string]any)
		if c["id"] != "icloud/icloud" {
			continue
		}
		creds, _ := c["credentials"].([]any)
		if len(creds) == 0 || creds[0].(map[string]any)["owner"] != "mail/icloud" || creds[0].(map[string]any)["pinned"] != true {
			t.Fatalf("web icloud/icloud credential view = %v, want the owner and pinned", c["credentials"])
		}
	}
	if deps := browser.do(http.MethodGet, "/web/v1/connections/dependencies", nil); deps.status != http.StatusOK || !strings.Contains(deps.body, "it uses the credential of mail/icloud; the credential is changed in host configuration") {
		t.Fatalf("the needs_reauth iCloud repair does not point at its owner: %d %s", deps.status, deps.body)
	}

	if after := row(); after != before {
		t.Fatalf("a refused save changed the credential row from %q to %q", before, after)
	}
	if got := audits(); got != audited {
		t.Fatalf("a refused save wrote settings audit rows: %s then %s", audited, got)
	}
}

func TestSavingAnUnpinnedMailPasswordRechecksTheICloudConnectionThatSharesIt(t *testing.T) {
	requireCipher(t)
	setEnv(t, "ATRIUM_DB_KEY", rightKeyHex)
	dir, _, f := moduleFixture(t, flagsExcept("", ""))
	srv := caldavtest.New(t, "operator@icloud.invalid", davPassword, "Home")
	setEnv(t, settingEnv(contacts.DirKey), filepath.Join(dir, "contacts"))
	setEnv(t, settingEnv(calendar.CalendarDirKey), filepath.Join(dir, "calendars"))
	seedSettings(t, map[string]string{davmirror.AppleIDKey: "operator@icloud.invalid"})
	host := hostConfigBodyWith("", defaultWebPushConnection, filepath.Join(testRoot, "host", "anthropic.key")) +
		"\n[connections.\"mail/icloud\"]\nkind = \"mail\"\nlabel = \"icloud\"\nconfig = { preset = \"icloud\", login = \"operator@icloud.invalid\", auth_method = \"password\", enabled = \"true\" }\ncapabilities = [\"imap\", \"smtp\"]\n" +
		"\n[connections.\"icloud/icloud\"]\nkind = \"icloud\"\nlabel = \"iCloud DAV\"\ncapabilities = [\"caldav\", \"carddav\"]\n"
	setEnv(t, "ATRIUM_HOST_CONFIG", writeFile(t, dir, "host.toml", host))
	t.Setenv(davmirror.CalDAVEnv, srv.URL+"/")
	t.Setenv(davmirror.CardDAVEnv, srv.URL+"/")
	if ran := runApp(t, "connections", "list"); ran.code != 0 {
		t.Fatalf("connections list: %d %s", ran.code, ran.stderr)
	}
	if view := testICloudConnection(t); view.State != "needs_reauth" {
		t.Fatalf("the iCloud connection with no mail password = %+v, want needs_reauth", view)
	}

	token := localAPIToken(t, "recheck")
	base, _, stop, err := startServeAppArgs(t, f.app, &lockedBuffer{}, "--auth", "local")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	s := &apiSite{t: t, base: base}
	settleBootChecks(t, dir, keyFor(t))
	if state := connectionState(t, dir, "icloud/icloud"); state != "needs_reauth" {
		t.Fatalf("icloud/icloud after the start checks = %q, want needs_reauth", state)
	}
	reason := ""
	if err := keyedDB(t, filepath.Join(dir, "atrium.db")).Read.QueryRow("SELECT reason FROM connections WHERE id = 'icloud/icloud'").Scan(&reason); err != nil || !strings.Contains(reason, "not set") {
		t.Fatalf("icloud/icloud reason = %q (%v), want the password is not set", reason, err)
	}
	if resp := s.do(http.MethodPost, "/api/v1/calendar/sync", token, nil); resp.status == http.StatusOK {
		t.Fatal("a calendar sync without a password succeeded")
	}
	spaced := davPassword[:6] + " " + davPassword[6:13] + " " + davPassword[13:]
	apiData(t, s.do(http.MethodPatch, "/api/v1/connections/mail/icloud", token, map[string]any{"credentials": map[string]string{"mail/icloud/password": spaced}}), http.StatusOK)
	waitFor(t, func() bool { return connectionState(t, dir, "icloud/icloud") == "ok" })
	apiData(t, s.do(http.MethodPost, "/api/v1/calendar/sync", token, nil), http.StatusOK)
}

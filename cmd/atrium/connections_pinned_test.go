package main

import (
	"fmt"
	"net/http"
	"net/url"
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
	expectAPI(t, browser.do(http.MethodPut, "/web/v1/connections/mail/icloud", body), http.StatusConflict, "refused")

	page := newWebClient(t, base, nil)
	page.session("local")
	status, html, _ := fetch(t, page.http, http.MethodGet, base+"/settings/connections", nil)
	if status != http.StatusOK || !strings.Contains(html, "Set in host configuration") || !strings.Contains(html, `data-owner="mail/icloud"`) ||
		strings.Contains(html, `name="credential.mail/icloud/password"`) {
		t.Fatalf("old connections page: %d, pinned shown %t, owner shown %t, entry box %t", status,
			strings.Contains(html, "Set in host configuration"), strings.Contains(html, `data-owner="mail/icloud"`), strings.Contains(html, `name="credential.mail/icloud/password"`))
	}
	if !strings.Contains(html, "it uses the credential of mail/icloud; the credential is changed in host configuration") {
		t.Fatal("the old connections page does not point the needs_reauth iCloud connection at its owner")
	}
	status, html, _ = fetch(t, page.http, http.MethodPost, base+"/settings/connections/test", url.Values{"id": {"mail/icloud"}, "credential.mail/icloud/password": {"ui-value"}})
	if status == http.StatusOK || !strings.Contains(html, "pinned by host configuration") || strings.Contains(html, "ui-value") {
		t.Fatalf("old connections form over a pinned slot: %d %.300s", status, html)
	}

	if after := row(); after != before {
		t.Fatalf("a refused save changed the credential row from %q to %q", before, after)
	}
	if got := audits(); got != audited {
		t.Fatalf("a refused save wrote settings audit rows: %s then %s", audited, got)
	}
}

package main

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/apps/calendar/caldavtest"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/davmirror"
)

func TestAContactsAppleIDLogsCardDAVInAsItsOwnUserWhileCalDAVKeepsTheAppleID(t *testing.T) {
	requireCipher(t)
	setEnv(t, "ATRIUM_DB_KEY", rightKeyHex)
	unsetEnv(t, settingEnv(davmirror.ContactsAppleIDKey))
	dir, srv := davFixture(t)
	sharedICloudHost(t, dir, davPassword)
	t.Setenv(davmirror.CalDAVEnv, srv.URL+"/")
	t.Setenv(davmirror.CardDAVEnv, srv.URL+"/")
	const contactsLogin = "contacts@icloud.invalid"
	srv.SetCardUser(contactsLogin)
	srv.SetObject(caldavtest.BookPath+"alice.vcf", aliceCard)

	if got := runApp(t, "contacts", "sync"); got.code == cli.ExitOK {
		t.Fatalf("contacts sync as the Apple ID = %d %q, want the refusal", got.code, got.stdout)
	}
	if state, reason, hold := storedCardDAV(t, dir); hold.Refusals != 1 {
		t.Fatalf("the refused contacts sync stored %s %q %+v, want one refusal held", state, reason, hold)
	}
	srv.TakeLogins()

	web := webG4(t, startServeIn(t, dir))
	if got := web.do(http.MethodPut, "/web/v1/settings/"+davmirror.ContactsAppleIDKey, map[string]any{"value": contactsLogin}); got.status != http.StatusOK {
		t.Fatalf("PUT /web/v1/settings/%s = %d: %.300s", davmirror.ContactsAppleIDKey, got.status, got.body)
	}

	if got := runApp(t, "contacts", "sync"); got.code != cli.ExitOK {
		t.Fatalf("contacts sync right after the contacts Apple ID was set = %d %q %q, want it to send and pass", got.code, got.stdout, got.stderr)
	}
	if _, err := os.Stat(filepath.Join(dir, "contacts", "card", "alice.vcf")); err != nil {
		t.Fatalf("the contacts sync did not mirror the card: %v", err)
	}
	view := testICloudConnection(t)
	for _, capability := range []string{davmirror.CapabilityCalDAV, davmirror.CapabilityCardDAV} {
		if state, reason := capabilityResult(view, capability); state != "ok" {
			t.Errorf("connections test %s = %s %q, want ok", capability, state, reason)
		}
	}
	for capability, source := range map[string]string{davmirror.CapabilityCardDAV: "the icloud_contacts_apple_id setting", davmirror.CapabilityCalDAV: "the icloud_apple_id setting"} {
		got := runApp(t, "connections", "test", "icloud/icloud", "--trace", capability)
		if got.code != cli.ExitOK || !strings.Contains(got.stdout, ", from "+source+"; the same as the mail login: ") {
			t.Errorf("traced %s test = %d %q, want a pass naming %s", capability, got.code, got.stdout, source)
		}
	}

	seen := map[string]bool{}
	for _, login := range srv.TakeLogins() {
		kind, user, _ := strings.Cut(login, " ")
		seen[kind] = true
		if want := map[string]string{"carddav": contactsLogin, "caldav": "operator@icloud.invalid"}[kind]; user != want {
			t.Errorf("a %s request logged in as %q, want %q", kind, user, want)
		}
	}
	if !seen["carddav"] || !seen["caldav"] {
		t.Errorf("requests seen per kind %v, want both carddav and caldav", seen)
	}
}

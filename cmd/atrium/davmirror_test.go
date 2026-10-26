package main

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/anthropic"
	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/calendar"
	"github.com/jeiang/atrium/internal/calendar/caldavtest"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/contacts"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/passrun"
)

const (
	aliceCard   = "BEGIN:VCARD\r\nVERSION:3.0\r\nUID:alice\r\nFN:Alice Example\r\nEMAIL:alice@example.com\r\nEND:VCARD\r\n"
	aliceMoved  = "BEGIN:VCARD\r\nVERSION:3.0\r\nUID:alice\r\nFN:Alice Example\r\nEMAIL:alice@new.example\r\nEND:VCARD\r\n"
	bobCard     = "BEGIN:VCARD\r\nVERSION:3.0\r\nUID:bob\r\nFN:Bob Builder\r\nEMAIL:bob@example.com\r\nEND:VCARD\r\n"
	dentistICS  = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\nBEGIN:VEVENT\r\nUID:dentist\r\nDTSTAMP:20261001T000000Z\r\nDTSTART:20261101T150000Z\r\nDTEND:20261101T160000Z\r\nSUMMARY:Dentist\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	davPassword = "dummy-icloud-password"
)

func passRows(t *testing.T, dir, job string) (ok, failed int, message string) {
	t.Helper()
	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
	if err := d.Read.QueryRow("SELECT COUNT(*) FILTER (WHERE outcome = 'ok'), COUNT(*) FILTER (WHERE outcome = 'failed'), "+
		"COALESCE((SELECT message FROM pass_runs WHERE job = ? ORDER BY id DESC LIMIT 1), '') FROM pass_runs WHERE job = ?", job, job).Scan(&ok, &failed, &message); err != nil {
		t.Fatal(err)
	}
	return ok, failed, message
}

func davFixture(t *testing.T) (dir string, srv *caldavtest.Server) {
	t.Helper()
	dir, _ = moduleFixture(t, flagsExcept("", ""))
	srv = caldavtest.New(t, "operator@icloud.invalid", davPassword, "Home")
	t.Cleanup(calendar.UseEndpoints(calendar.Endpoints{CalDAV: srv.URL + "/", CardDAV: srv.URL + "/"}))
	t.Setenv(settingEnv(contacts.DirKey), filepath.Join(dir, "contacts"))
	t.Setenv(settingEnv(calendar.CalendarDirKey), filepath.Join(dir, "calendars"))
	return dir, srv
}

func lookup(t *testing.T, query string) string {
	t.Helper()
	api := &modelLog{Server: anthropictest.New(t,
		anthropictest.Reply("tool_use", anthropictest.ToolUse("c1", "contacts_lookup", map[string]any{"query": query})),
		anthropictest.Reply("end_turn", anthropictest.Text("done")),
	)}
	api.OnRequest = func(r anthropictest.Request) {
		api.mu.Lock()
		defer api.mu.Unlock()
		api.seen = append(api.seen, r)
	}
	t.Setenv(anthropic.BaseURLEnv, api.URL)
	if ran := runApp("agent", "run", "--text", "who is "+query, "--kind", "chat"); ran.code != cli.ExitOK {
		t.Fatalf("agent run: %d %s", ran.code, ran.stderr)
	}
	requests := api.Requests()
	if len(requests) != 2 {
		t.Fatalf("%d model requests, want 2", len(requests))
	}
	return string(requests[1].Body)
}

func TestContactsSyncMirrorsTheAddressBookForLookup(t *testing.T) {
	dir, srv := davFixture(t)
	srv.SetObject(caldavtest.BookPath+"alice.vcf", aliceCard)

	if got := runApp("contacts", "sync"); got.code != cli.ExitOK || !strings.Contains(got.stdout, `"written":1`) {
		t.Fatalf("first sync: %d %q %q", got.code, got.stdout, got.stderr)
	}
	if result := lookup(t, "Alice"); !strings.Contains(result, "alice@example.com") {
		t.Errorf("lookup after the first sync lacks alice@example.com")
	}

	srv.SetObject(caldavtest.BookPath+"alice.vcf", aliceMoved)
	srv.SetObject(caldavtest.BookPath+"bob.vcf", bobCard)
	if got := runApp("contacts", "sync"); got.code != cli.ExitOK {
		t.Fatalf("second sync: %d %q", got.code, got.stderr)
	}
	if result := lookup(t, "Alice"); !strings.Contains(result, "alice@new.example") || strings.Contains(result, "alice@example.com") {
		t.Errorf("lookup after the update does not show the new address")
	}
	if result := lookup(t, "Bob"); !strings.Contains(result, "bob@example.com") {
		t.Errorf("lookup lacks the added contact")
	}

	srv.RemoveObject(caldavtest.BookPath + "bob.vcf")
	if got := runApp("contacts", "sync"); got.code != cli.ExitOK || !strings.Contains(got.stdout, `"removed":1`) {
		t.Fatalf("third sync: %d %q %q", got.code, got.stdout, got.stderr)
	}
	if _, err := os.Stat(filepath.Join(dir, "contacts", "card", "bob.vcf")); !os.IsNotExist(err) {
		t.Errorf("a contact deleted on the server is still mirrored: %v", err)
	}
	if got := runApp("contacts", "sync"); got.code != cli.ExitOK || !strings.Contains(got.stdout, `"written":0`) {
		t.Errorf("an unchanged address book was rewritten: %q", got.stdout)
	}
	if ok, _, _ := passRows(t, dir, passrun.CardDAV); ok != 4 {
		t.Errorf("carddav pass rows ok=%d, want 4", ok)
	}
}

func TestCalendarSyncSignsInWithTheICloudMailAccountAndBadCredentialsChangeNothing(t *testing.T) {
	dir, srv := davFixture(t)
	srv.SetObject(caldavtest.CalendarPath(0)+"dentist.ics", dentistICS)
	if got := runApp("calendar", "sync"); got.code != cli.ExitOK {
		t.Fatalf("calendar sync: %d %q", got.code, got.stderr)
	}
	mirrored := filepath.Join(dir, "calendars", "cal0", "dentist.ics")
	data, err := os.ReadFile(mirrored)
	if err != nil || !strings.Contains(string(data), "SUMMARY:Dentist") {
		t.Fatalf("mirrored event %q, %v", data, err)
	}

	srv.RemoveObject(caldavtest.CalendarPath(0) + "dentist.ics")
	t.Setenv("ICLOUD_APP_PASSWORD", davPassword)
	t.Setenv(settingEnv(config.KeyMailAccountsFile), accountsFile(t, dir, mailAccount{"icloud", "icloud", "operator@icloud.invalid", "wrong", false}))
	if got := runApp("calendar", "sync"); got.code == cli.ExitOK || !strings.Contains(got.stderr, "401") {
		t.Fatalf("sync with a bad password: %d %q", got.code, got.stderr)
	}
	if _, err := os.Stat(mirrored); err != nil {
		t.Errorf("a failed sync changed the mirror: %v", err)
	}
	if _, failed, _ := passRows(t, dir, passrun.CalDAV); failed != 1 {
		t.Errorf("caldav failed rows = %d, want 1", failed)
	}

	t.Setenv(settingEnv(calendar.AppleIDKey), "")
	t.Setenv(settingEnv(config.KeyMailAccountsFile), accountsFile(t, dir,
		mailAccount{"work", "icloud", "work@icloud.invalid", "wrong", false},
		mailAccount{"home", "icloud", "operator@icloud.invalid", davPassword, false}))
	if got := runApp("calendar", "sync"); !strings.Contains(got.stderr, "needs-settings") || !strings.Contains(got.stderr, "several iCloud mail accounts; set calendar_account") {
		t.Fatalf("sync with two iCloud accounts and no calendar_account was not held for settings: %d %q", got.code, got.stderr)
	}
	t.Setenv(settingEnv(calendar.AccountKey), "home")
	if got := runApp("calendar", "sync"); got.code != cli.ExitOK {
		t.Fatalf("calendar sync: %d %q", got.code, got.stderr)
	}
	if _, err := os.Stat(mirrored); !os.IsNotExist(err) {
		t.Errorf("an event deleted on the server is still mirrored: %v", err)
	}
}

func TestTriageSaysSoWhenThereIsNoContactSource(t *testing.T) {
	for _, on := range []string{"true", "false"} {
		t.Run("contacts_"+on, func(t *testing.T) {
			dir, _ := moduleFixture(t, flagsExcept(module.Contacts, on))
			base := startServeIn(t, dir)
			status, body, _ := fetch(t, http.DefaultClient, "GET", base+"/triage", nil)
			if status != http.StatusOK {
				t.Fatalf("GET /triage = %d", status)
			}
			if shown := strings.Contains(body, "No contact source is configured"); shown != (on == "false") {
				t.Errorf("contacts %s: explanation shown = %v", on, shown)
			}
		})
	}
}

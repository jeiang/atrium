package main

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/anthropic"
	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/calendar"
	"github.com/jeiang/atrium/internal/calendar/caldavtest"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/contacts"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/passrun"
	"github.com/jeiang/atrium/internal/tool"
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
	dir, _, _ = moduleFixture(t, flagsExcept("", ""))
	srv = caldavtest.New(t, "operator@icloud.invalid", davPassword, "Home")
	setEnv(t, calendar.CalDAVEnv, srv.URL+"/")
	setEnv(t, calendar.CardDAVEnv, srv.URL+"/")
	setEnv(t, settingEnv(contacts.DirKey), filepath.Join(dir, "contacts"))
	setEnv(t, settingEnv(calendar.CalendarDirKey), filepath.Join(dir, "calendars"))
	seedSettings(t, map[string]string{calendar.AppleIDKey: "operator@icloud.invalid"})
	return dir, srv
}

func lookup(t *testing.T, query string) string {
	t.Helper()
	api := &modelLog{Server: anthropictest.New(t,
		anthropictest.Reply("tool_use", anthropictest.ToolUse("c1", "contacts_lookup", map[string]any{"query": query, "include": []any{"emails"}})),
		anthropictest.Reply("end_turn", anthropictest.Text("done")),
	)}
	api.OnRequest = func(r anthropictest.Request) {
		api.mu.Lock()
		defer api.mu.Unlock()
		api.seen = append(api.seen, r)
	}
	setEnv(t, anthropic.BaseURLEnv, api.URL)
	if ran := runApp(t, "agent", "run", "--text", "who is "+query, "--kind", "chat"); ran.code != cli.ExitOK {
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

	if got := runApp(t, "contacts", "sync"); got.code != cli.ExitOK || !strings.Contains(got.stdout, `"written":1`) {
		t.Fatalf("first sync: %d %q %q", got.code, got.stdout, got.stderr)
	}
	if result := lookup(t, "Alice"); !strings.Contains(result, "alice@example.com") {
		t.Errorf("lookup after the first sync lacks alice@example.com")
	}

	srv.SetObject(caldavtest.BookPath+"alice.vcf", aliceMoved)
	srv.SetObject(caldavtest.BookPath+"bob.vcf", bobCard)
	if got := runApp(t, "contacts", "sync"); got.code != cli.ExitOK {
		t.Fatalf("second sync: %d %q", got.code, got.stderr)
	}
	if result := lookup(t, "Alice"); !strings.Contains(result, "alice@new.example") || strings.Contains(result, "alice@example.com") {
		t.Errorf("lookup after the update does not show the new address")
	}
	if result := lookup(t, "Bob"); !strings.Contains(result, "bob@example.com") {
		t.Errorf("lookup lacks the added contact")
	}

	srv.RemoveObject(caldavtest.BookPath + "bob.vcf")
	if got := runApp(t, "contacts", "sync"); got.code != cli.ExitOK || !strings.Contains(got.stdout, `"removed":1`) {
		t.Fatalf("third sync: %d %q %q", got.code, got.stdout, got.stderr)
	}
	if _, err := os.Stat(filepath.Join(dir, "contacts", "card", "bob.vcf")); !os.IsNotExist(err) {
		t.Errorf("a contact deleted on the server is still mirrored: %v", err)
	}
	if got := runApp(t, "contacts", "sync"); got.code != cli.ExitOK || !strings.Contains(got.stdout, `"written":0`) {
		t.Errorf("an unchanged address book was rewritten: %q", got.stdout)
	}
	if ok, _, _ := passRows(t, dir, passrun.CardDAV); ok != 4 {
		t.Errorf("carddav pass rows ok=%d, want 4", ok)
	}
}

func TestCalendarSyncSignsInWithTheICloudMailAccountAndBadCredentialsChangeNothing(t *testing.T) {
	dir, srv := davFixture(t)
	srv.SetObject(caldavtest.CalendarPath(0)+"dentist.ics", dentistICS)
	if got := runApp(t, "calendar", "sync"); got.code != cli.ExitOK {
		t.Fatalf("calendar sync: %d %q", got.code, got.stderr)
	}
	mirrored := filepath.Join(dir, "calendars", "cal0", "dentist.ics")
	data, err := os.ReadFile(mirrored)
	if err != nil || !strings.Contains(string(data), "SUMMARY:Dentist") {
		t.Fatalf("mirrored event %q, %v", data, err)
	}

	srv.RemoveObject(caldavtest.CalendarPath(0) + "dentist.ics")
	setEnv(t, "ICLOUD_APP_PASSWORD", davPassword)
	setEnv(t, "ATRIUM_HOST_CONFIG", accountsFile(t, dir, mailAccount{"icloud", "icloud", "operator@icloud.invalid", "wrong", false}))
	if got := runApp(t, "calendar", "sync"); got.code == cli.ExitOK || !strings.Contains(got.stderr, "401") {
		t.Fatalf("sync with a bad password: %d %q", got.code, got.stderr)
	}
	if _, err := os.Stat(mirrored); err != nil {
		t.Errorf("a failed sync changed the mirror: %v", err)
	}
	if _, failed, _ := passRows(t, dir, passrun.CalDAV); failed != 1 {
		t.Errorf("caldav failed rows = %d, want 1", failed)
	}

	seedSettings(t, map[string]string{calendar.AppleIDKey: ""})
	setEnv(t, "ATRIUM_HOST_CONFIG", accountsFile(t, dir,
		mailAccount{"work", "icloud", "work@icloud.invalid", "wrong", false},
		mailAccount{"home", "icloud", "operator@icloud.invalid", davPassword, false}))
	if got := runAppLogged(t, "calendar", "sync"); !strings.Contains(got.stderr, "needs-settings") || !strings.Contains(got.stderr, "several iCloud mail accounts; set calendar_account") {
		t.Fatalf("sync with two iCloud accounts and no calendar_account was not held for settings: %d %q", got.code, got.stderr)
	}
	seedSettings(t, map[string]string{calendar.AccountKey: "home"})
	if got := runApp(t, "calendar", "sync"); got.code != cli.ExitOK {
		t.Fatalf("calendar sync: %d %q", got.code, got.stderr)
	}
	if _, err := os.Stat(mirrored); !os.IsNotExist(err) {
		t.Errorf("an event deleted on the server is still mirrored: %v", err)
	}
}

func TestTriageSaysSoWhenThereIsNoContactSource(t *testing.T) {
	for _, on := range []string{"true", "false"} {
		t.Run("contacts_"+on, func(t *testing.T) {
			dir, _, _ := moduleFixture(t, flagsExcept(module.Contacts, on))
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

func TestContactNameLookupKeepsEgressAndIdentifiersDropIt(t *testing.T) {
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	contactsDir := filepath.Join(dir, "contacts")
	setEnv(t, settingEnv(contacts.DirKey), contactsDir)
	if err := os.MkdirAll(filepath.Join(contactsDir, "card"), 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(contactsDir, "card", "alice.vcf"), []byte(aliceCard), 0o666); err != nil {
		t.Fatal(err)
	}
	start := warm(t, api)
	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("c1", "contacts_lookup", map[string]any{"query": "Alice"})))
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("named")))
	if ran := runApp(t, "agent", "run", "--text", "who is Alice"); ran.code != cli.ExitOK {
		t.Fatalf("name lookup run: %d %s", ran.code, ran.stderr)
	}
	requests := api.Requests()[start:]
	if len(requests) != 2 {
		t.Fatalf("%d model requests, want 2", len(requests))
	}
	if !slices.Contains(requestTools(t, requests[0]), "web_fetch") {
		t.Fatalf("web_fetch not offered before the lookup: %v", requestTools(t, requests[0]))
	}
	if after := requestTools(t, requests[1]); !slices.Contains(after, "web_fetch") {
		t.Errorf("a name lookup dropped web_fetch: %v", after)
	}
	content := fmt.Sprint(toolResultFor(t, requests[1], "c1")["content"])
	if strings.Contains(content, "alice@example.com") {
		t.Errorf("the default lookup returned an email: %s", content)
	}
	if !strings.Contains(content, "Alice Example") {
		t.Errorf("the lookup result lacks the name: %s", content)
	}
	thread := latestThread(t, dir)
	if marks := threadMarkSet(t, dir, thread); marks != 0 {
		t.Errorf("thread marks after a name lookup = %d, want 0", marks)
	}

	start = len(api.Requests())
	api.Push(
		anthropictest.Reply("tool_use", anthropictest.ToolUse("c2", "contacts_lookup", map[string]any{"query": "Alice", "include": []any{"emails"}})),
		anthropictest.Reply("tool_use", anthropictest.ToolUse("f1", "web_fetch", map[string]any{"url": "https://example.com/"})),
		anthropictest.Reply("end_turn", anthropictest.Text("done")),
	)
	if ran := runApp(t, "agent", "run", "--thread", fmt.Sprint(thread), "--text", "and her email?"); ran.code != cli.ExitOK {
		t.Fatalf("identifier lookup run: %d %s", ran.code, ran.stderr)
	}
	requests = api.Requests()[start:]
	if len(requests) != 3 {
		t.Fatalf("%d model requests, want 3", len(requests))
	}
	if !slices.Contains(requestTools(t, requests[0]), "web_fetch") {
		t.Fatalf("the second run did not start with web_fetch: %v", requestTools(t, requests[0]))
	}
	content = fmt.Sprint(toolResultFor(t, requests[1], "c2")["content"])
	if !strings.Contains(content, "alice@example.com") {
		t.Errorf("the include lookup did not return the email: %s", content)
	}
	refusal := toolResultFor(t, requests[2], "f1")
	if refusal["is_error"] != true || !strings.Contains(fmt.Sprint(refusal["content"]), "web_fetch is not allowed in this thread by the permission table") {
		t.Errorf("web_fetch after an identifier lookup returned %v, want a refusal", refusal)
	}
	if marks := threadMarkSet(t, dir, thread); marks != int64(tool.PII) {
		t.Errorf("thread marks after an identifier lookup = %d, want pii", marks)
	}
}

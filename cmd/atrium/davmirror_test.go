package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/anthropic"
	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/apps/calendar"
	"github.com/jeiang/atrium/internal/apps/calendar/caldavtest"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/contacts"
	"github.com/jeiang/atrium/internal/davmirror"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/passrun"
	"github.com/jeiang/atrium/internal/tool"
	"github.com/jeiang/atrium/internal/unit"
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
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), db.Key{})
	if err := d.Read.QueryRow("SELECT COUNT(*) FILTER (WHERE r.status = 'ok'), COUNT(*) FILTER (WHERE r.status = 'error'), "+
		"COALESCE((SELECT x.error FROM job_runs x JOIN jobs y ON y.id = x.job_id WHERE y.name = ? AND x.status = 'error' ORDER BY x.id DESC LIMIT 1), '') "+
		"FROM job_runs r JOIN jobs j ON j.id = r.job_id WHERE j.name = ?", job, job).Scan(&ok, &failed, &message); err != nil {
		t.Fatal(err)
	}
	return ok, failed, message
}

func davFixture(t *testing.T) (dir string, srv *caldavtest.Server) {
	t.Helper()
	dir, _, _ = moduleFixture(t, flagsExcept("", ""))
	srv = caldavtest.New(t, "operator@icloud.invalid", davPassword, "Home")
	setEnv(t, davmirror.CalDAVEnv, srv.URL+"/")
	setEnv(t, davmirror.CardDAVEnv, srv.URL+"/")
	setEnv(t, settingEnv(contacts.DirKey), filepath.Join(dir, "contacts"))
	setEnv(t, settingEnv(calendar.CalendarDirKey), filepath.Join(dir, "calendars"))
	seedSettings(t, map[string]string{davmirror.AppleIDKey: "operator@icloud.invalid"})
	return dir, srv
}

func lookup(t *testing.T, query string) string {
	t.Helper()
	api := &modelLog{Server: anthropictest.New(t,
		anthropictest.Reply("tool_use", anthropictest.ToolUse("c1", "contacts_lookup", map[string]any{"query": query, "include": []any{"emails"}})),
		anthropictest.Reply("end_turn", anthropictest.Text("done")),
	)}
	api.SetOnRequest(func(r anthropictest.Request) {
		api.mu.Lock()
		defer api.mu.Unlock()
		api.seen = append(api.seen, r)
	})
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
	t.Parallel()
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
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
	defer func() { _ = d.Close() }()
	var audits int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM operation_audit WHERE operation = 'contacts.sync' AND outcome = 'ok' AND actor = 'operator' AND source = 'cli'").Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if audits != 4 {
		t.Fatalf("contacts sync wrote %d operator/cli success audits, want 4", audits)
	}
}

func TestContactsSyncSkipsOffAndUnconfiguredWithoutAuditing(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, enabled string
	}{
		{"off", "false"},
		{"unconfigured", "true"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, _, _ := moduleFixture(t, flagsExcept("contacts", tc.enabled))
			contactsDir := ""
			if tc.name == "off" {
				contactsDir = filepath.Join(dir, "contacts")
			}
			setEnv(t, settingEnv(contacts.DirKey), contactsDir)
			got := runApp(t, "contacts", "sync")
			if got.code != cli.ExitOK || got.stdout != "" {
				t.Fatalf("contacts sync = %d: %s", got.code, got.stderr)
			}
			d := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
			defer func() { _ = d.Close() }()
			var audits int
			if err := d.Read.QueryRow("SELECT COUNT(*) FROM operation_audit WHERE operation = 'contacts.sync'").Scan(&audits); err != nil {
				t.Fatal(err)
			}
			if audits != 0 {
				t.Fatalf("skipped contacts sync wrote %d audits", audits)
			}
		})
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
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
	defer func() { _ = d.Close() }()
	if audits := operationAudits(t, d, "calendar.sync"); len(audits) != 1 || !strings.HasPrefix(audits[0], "ok ") {
		t.Fatalf("calendar.sync audits after a sync that wrote an event = %q, want one ok row", audits)
	}
	if got := runApp(t, "calendar", "sync"); got.code != cli.ExitOK {
		t.Fatalf("unchanged calendar sync: %d %q", got.code, got.stderr)
	}
	if audits := operationAudits(t, d, "calendar.sync"); len(audits) != 1 {
		t.Fatalf("calendar.sync audits after a sync that changed nothing = %q, want no new row", audits)
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
	if audits := operationAudits(t, d, "calendar.sync"); len(audits) != 2 || strings.HasPrefix(audits[1], "ok ") {
		t.Errorf("calendar.sync audits after a failed sync = %q, want one more non-ok row", audits)
	}

	seedSettings(t, map[string]string{davmirror.AppleIDKey: ""})
	setEnv(t, "ATRIUM_HOST_CONFIG", accountsFile(t, dir,
		mailAccount{"work", "icloud", "work@icloud.invalid", "wrong", false},
		mailAccount{"home", "icloud", "operator@icloud.invalid", davPassword, false}))
	if got := runAppLogged(t, "calendar", "sync"); !strings.Contains(got.stderr, "needs-settings") || !strings.Contains(got.stderr, "several iCloud mail accounts; set calendar_account") {
		t.Fatalf("sync with two iCloud accounts and no calendar_account was not held for settings: %d %q", got.code, got.stderr)
	}
	seedSettings(t, map[string]string{davmirror.AccountKey: "home"})
	if got := runApp(t, "calendar", "sync"); got.code != cli.ExitOK {
		t.Fatalf("calendar sync: %d %q", got.code, got.stderr)
	}
	if _, err := os.Stat(mirrored); !os.IsNotExist(err) {
		t.Errorf("an event deleted on the server is still mirrored: %v", err)
	}
}

func TestContactNameLookupKeepsEgressAndIdentifiersDropIt(t *testing.T) {
	t.Parallel()
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	denyMarkedInternet(t)
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

func sharedICloudHost(t *testing.T, dir, password string) {
	t.Helper()
	host := accountsFile(t, dir, mailAccount{"icloud", "icloud", "operator@icloud.invalid", password, false})
	f, err := os.OpenFile(host, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintf(f, `
[connections."icloud/icloud"]
kind = "icloud"
label = "iCloud DAV"
capabilities = ["caldav", "carddav"]
credential_files = { "mail/icloud/password" = %q }
`, filepath.Join(dir, "icloud.password")); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	setEnv(t, "ATRIUM_HOST_CONFIG", host)
}

type icloudTest struct {
	ID       string `json:"id"`
	Enabled  bool   `json:"enabled"`
	State    string `json:"state"`
	Reason   string `json:"reason"`
	ReauthAt string `json:"reauth_at"`
	Results  []struct {
		Capability string `json:"capability"`
		State      string `json:"state"`
		Reason     string `json:"reason"`
	} `json:"results"`
}

func testICloudConnection(t *testing.T) icloudTest {
	t.Helper()
	ran := runApp(t, "connections", "test", "icloud/icloud", "--json")
	var views []icloudTest
	if err := json.Unmarshal([]byte(ran.stdout), &views); err != nil || len(views) != 1 {
		t.Fatalf("connections test icloud/icloud: exit %d stdout %q stderr %q: %v", ran.code, ran.stdout, ran.stderr, err)
	}
	return views[0]
}

func icloudRows(t *testing.T, dir string) (rows, credentials int) {
	t.Helper()
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
	defer func() { _ = d.Close() }()
	if err := d.Read.QueryRow("SELECT (SELECT COUNT(*) FROM connections WHERE kind = 'icloud'), "+
		"(SELECT COUNT(*) FROM connection_credentials c JOIN connections k ON k.id = c.connection_id WHERE k.kind = 'icloud')").Scan(&rows, &credentials); err != nil {
		t.Fatal(err)
	}
	return rows, credentials
}

func TestOneICloudConnectionServesContactsAndCalendarWhileEitherUnitIsOff(t *testing.T) {
	requireCipher(t)
	setEnv(t, "ATRIUM_DB_KEY", rightKeyHex)
	dir, srv := davFixture(t)
	sharedICloudHost(t, dir, davPassword)
	t.Setenv(davmirror.CalDAVEnv, srv.URL+"/")
	t.Setenv(davmirror.CardDAVEnv, srv.URL+"/")
	srv.SetObject(caldavtest.BookPath+"alice.vcf", aliceCard)
	srv.SetObject(caldavtest.CalendarPath(0)+"dentist.ics", dentistICS)
	card := filepath.Join(dir, "contacts", "card", "alice.vcf")
	event := filepath.Join(dir, "calendars", "cal0", "dentist.ics")

	view := testICloudConnection(t)
	if view.State != "ok" || len(view.Results) != 2 {
		t.Fatalf("shared iCloud connection test = %+v, want ok for caldav and carddav", view)
	}
	for _, args := range [][]string{{"contacts", "sync"}, {"calendar", "sync"}} {
		if got := runApp(t, args...); got.code != cli.ExitOK {
			t.Fatalf("%v: %d %q", args, got.code, got.stderr)
		}
	}
	for _, file := range []string{card, event} {
		if _, err := os.Stat(file); err != nil {
			t.Fatalf("one shared connection did not mirror %s: %v", file, err)
		}
	}
	if rows, credentials := icloudRows(t, dir); rows != 1 || credentials != 1 {
		t.Fatalf("icloud rows = %d with %d credentials, want one row with one credential", rows, credentials)
	}

	for _, tc := range []struct {
		off, on     string
		args        []string
		server, out string
	}{
		{unit.Calendar, unit.Contacts, []string{"contacts", "sync"}, caldavtest.BookPath + "bob.vcf", filepath.Join(dir, "contacts", "card", "bob.vcf")},
		{unit.Contacts, unit.Calendar, []string{"calendar", "sync"}, caldavtest.CalendarPath(0) + "lunch.ics", filepath.Join(dir, "calendars", "cal0", "lunch.ics")},
	} {
		seedSettings(t, map[string]string{module.FlagKey(tc.off): "false", module.FlagKey(tc.on): "true"})
		if strings.HasSuffix(tc.server, ".vcf") {
			srv.SetObject(tc.server, bobCard)
		} else {
			srv.SetObject(tc.server, strings.ReplaceAll(strings.ReplaceAll(dentistICS, "dentist", "lunch"), "Dentist", "Lunch"))
		}
		if got := runApp(t, tc.args...); got.code != cli.ExitOK || !strings.Contains(got.stdout, `"written":1`) {
			t.Fatalf("%s off, %v: %d %q %q", tc.off, tc.args, got.code, got.stdout, got.stderr)
		}
		if _, err := os.Stat(tc.out); err != nil {
			t.Fatalf("%s off, %v did not mirror: %v", tc.off, tc.args, err)
		}
		if view := testICloudConnection(t); !view.Enabled || view.State != "ok" {
			t.Fatalf("%s off: the shared connection is %+v, want enabled and ok", tc.off, view)
		}
	}
	seedSettings(t, map[string]string{module.FlagKey(unit.Calendar): "true", module.FlagKey(unit.Contacts): "true"})

	label, _, _ := runtimeRow(t, dir, "icloud/icloud")
	notices := connectionNotices(t, dir, label)
	sharedICloudHost(t, dir, "wrong")
	view = testICloudConnection(t)
	if view.State != "needs_reauth" || view.Reason == "" || len(view.Results) != 2 {
		t.Fatalf("a wrong password gave %+v, want one needs_reauth row with both capabilities", view)
	}
	for _, r := range view.Results {
		if r.State != "needs_reauth" || !strings.HasPrefix(r.Reason, "iCloud refused the app password; atrium sends iCloud no request for this until ") {
			t.Errorf("capability %s after a wrong password = %s %q", r.Capability, r.State, r.Reason)
		}
	}
	if n := connectionNotices(t, dir, label) - notices; n != 1 {
		t.Errorf("%d repair notices after a wrong password, want 1", n)
	}
	for _, args := range [][]string{{"contacts", "sync"}, {"calendar", "sync"}} {
		if got := runApp(t, args...); got.code == cli.ExitOK || !strings.Contains(got.stderr, "skipped, backing off until ") {
			t.Errorf("%v with a wrong password: %d %q", args, got.code, got.stderr)
		}
	}
	if rows, credentials := icloudRows(t, dir); rows != 1 || credentials != 1 {
		t.Fatalf("icloud rows after a wrong password = %d with %d credentials, want one row with one credential", rows, credentials)
	}
	since := view.ReauthAt
	if since == "" {
		t.Fatalf("a wrong password left no sign-in time: %+v", view)
	}
	if again := testICloudConnection(t); again.ReauthAt != since {
		t.Errorf("a second refusal moved the sign-in time from %s to %s", since, again.ReauthAt)
	}
	sharedICloudHost(t, dir, davPassword)
	if view = testICloudConnection(t); view.State != "ok" || view.ReauthAt != "" {
		t.Errorf("after the right password the connection is %s with sign-in time %q, want ok and none", view.State, view.ReauthAt)
	}
}

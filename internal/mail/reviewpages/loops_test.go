package reviewpages

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/mail/openloops"
)

const day = 86400.0

func (s *site) loop(key, state string, ageDays float64, subject, counterpart string, message int) {
	s.t.Helper()
	now := s.clock
	s.exec("INSERT INTO open_loops (thread_key, account, state, last_message_id, since, counterpart, subject, updated_ts) VALUES (?, 'icloud', ?, ?, ?, ?, ?, ?)",
		key, state, message, now-ageDays*day, counterpart, subject, now)
}

func (s *site) loopID(key string) int64 {
	s.t.Helper()
	var id int64
	if err := s.db.Write.QueryRow("SELECT rowid FROM open_loops WHERE thread_key = ?", key).Scan(&id); err != nil {
		s.t.Fatal(err)
	}
	return id
}

func TestLoopsPageRendersAndActs(t *testing.T) {
	s := newSite(t)
	s.loop("a", "owe_reply", 2, "Dinner?", "pat@person.test", 1)
	s.loop("b", "waiting_on_them", 10, "Quote please", "lee@other.test", 2)
	s.exec("INSERT INTO commitments (message_id, text, owner, due, created_ts) VALUES (1, 'Bring wine', 'me', '2026-11-01', 0)")
	page := status(t, s.get("/loops"), http.StatusOK)
	mustContain(t, page, "Dinner?", "Quote please", "Bring wine", "Draft a reply to mail 1", "Snooze", "Dismiss", "You owe a reply (1)", "Waiting on them (1)",
		"Commitments (1)", "2 days", "10 days", `data-confirm-submit="Dismiss all 1? Each comes back only if its thread gets new mail."`, "Due 2026-11-01")
	for _, section := range []string{"3 days</option>", `<option value="3" selected>`} {
		if !strings.Contains(page, section) {
			t.Errorf("snooze choices miss %q", section)
		}
	}
	id := s.loopID("a")
	body := status(t, s.post(fmt.Sprintf("/loops/%d/snooze", id), form("days", "7")), http.StatusOK)
	mustContain(t, body, "Snoozed", "Hidden for 7 days.")
	mustNotContain(t, body, "Draft a reply to mail 1")
	done := status(t, s.post("/loops/commitments/1/done", nil), http.StatusOK)
	mustContain(t, done, "Marked done", "No open commitments")
	mustNotContain(t, done, "Bring wine")
	status(t, s.post("/loops/999/dismiss", nil), http.StatusNotFound)
}

func TestSnoozeClampsItsDays(t *testing.T) {
	s := newSite(t)
	s.loop("a", "owe_reply", 2, "Dinner?", "pat@person.test", 1)
	id := s.loopID("a")
	target := fmt.Sprintf("/loops/%d/snooze", id)
	mustContain(t, status(t, s.post(target, form("days", "0")), http.StatusOK), "Hidden for 1 day.")
	mustContain(t, status(t, s.post(target, form("days", "500")), http.StatusOK), "Hidden for 60 days.")
	var until float64
	if err := s.db.Write.QueryRow("SELECT snoozed_until FROM open_loops WHERE rowid = ?", id).Scan(&until); err != nil {
		t.Fatal(err)
	}
	if want := s.clock + 60*day; until < want-100 || until > want+100 {
		t.Errorf("snoozed_until = %v, want about %v", until, want)
	}
	mustContain(t, status(t, s.post(target, nil), http.StatusOK), fmt.Sprintf("Hidden for %d days.", openloops.SnoozeDefaultDays))
	bad := s.post(target, form("days", "soon"))
	body := status(t, bad, http.StatusUnprocessableEntity)
	if detail := jsonOf(t, body)["detail"].([]any)[0].(map[string]any); detail["loc"].([]any)[0] != "body" {
		t.Errorf("detail = %v", detail)
	}
}

func TestDismissAndDismissAll(t *testing.T) {
	s := newSite(t)
	s.loop("a", "owe_reply", 2, "Dinner?", "pat@person.test", 1)
	s.loop("b", "owe_reply", 3, "Lunch?", "pat@person.test", 2)
	s.loop("c", "waiting_on_them", 4, "Quote", "lee@other.test", 3)
	body := status(t, s.post(fmt.Sprintf("/loops/%d/dismiss", s.loopID("a")), nil), http.StatusOK)
	mustContain(t, body, "Dismissed", "You owe a reply (1)")
	mustNotContain(t, body, "Dinner?")
	all := status(t, s.post("/loops/dismiss-all", form("state", "owe_reply")), http.StatusOK)
	mustContain(t, all, "1 loop cleared.", "No one is waiting on you.", "Quote")
	mustContain(t, status(t, s.post("/loops/dismiss-all", form("state", "waiting_on_them")), http.StatusOK), "1 loop cleared.")
	mustContain(t, status(t, s.post("/loops/dismiss-all", form("state", "waiting_on_them")), http.StatusOK), "0 loops cleared.")
	bad := status(t, s.post("/loops/dismiss-all", form("state", "bogus")), http.StatusBadRequest)
	if jsonOf(t, bad)["detail"] != "unknown state" {
		t.Errorf("body = %s", bad)
	}
	status(t, s.post("/loops/dismiss-all", nil), http.StatusUnprocessableEntity)
}

func TestCommitmentOutcomes(t *testing.T) {
	s := newSite(t)
	s.exec("INSERT INTO commitments (message_id, text, owner, due, created_ts) VALUES (1, 'Bring wine', 'me', NULL, 0), (2, 'Call back', 'them', NULL, 0)")
	mustContain(t, status(t, s.post("/loops/commitments/1/dismiss", nil), http.StatusOK), "Dismissed", "Call back")
	status(t, s.post("/loops/commitments/2/ignore", nil), http.StatusNotFound)
	status(t, s.post("/loops/commitments/99/done", nil), http.StatusNotFound)
	status(t, s.post("/loops/commitments/x/done", nil), http.StatusUnprocessableEntity)
	var open int
	if err := s.db.Write.QueryRow("SELECT COUNT(*) FROM commitments WHERE status = 'open'").Scan(&open); err != nil || open != 1 {
		t.Errorf("open = %d, %v", open, err)
	}
}

func TestEmptyLoopsPage(t *testing.T) {
	s := newSite(t)
	mustContain(t, status(t, s.get("/loops"), http.StatusOK), "No one is waiting on you.", "No sent mail is waiting for an answer.",
		"No open commitments", "You owe a reply (0)")
}

func (s *site) package_(tracking, status string, updated float64, dismissed int) {
	s.t.Helper()
	s.exec("INSERT INTO packages (carrier, tracking_number, merchant, status, status_text, source_message_id, last_message_id, updated_ts, first_seen, dismissed) "+
		"VALUES ('UPS', ?, 'Shop', ?, 'On its way', 1, 2, ?, ?, ?)", tracking, status, updated, updated, dismissed)
}

func TestWatchesPageAndPackageDismissal(t *testing.T) {
	s := newSite(t)
	s.package_("1Z999AA10123456784", "in_transit", s.clock-3600, 0)
	s.package_("DELIVERED1", "delivered", s.clock-3600, 0)
	page := status(t, s.get("/watches"), http.StatusOK)
	mustContain(t, page, "1Z999AA10123456784", "Address book not configured", "ATRIUM_CONTACTS_DIR is not set.", `href="/money/bills"`,
		"Packages in transit (1)", "in transit", `href="/messages/2"`, "Birthdays and anniversaries, next 60 days</h2>")
	mustNotContain(t, page, "DELIVERED1", "Renewals")
	body := status(t, s.post("/watches/packages/1/dismiss", nil), http.StatusOK)
	mustContain(t, body, "Dismissed", "No packages in transit")
	mustNotContain(t, body, "1Z999AA10123456784")
	status(t, s.post("/watches/renewals/99/dismiss", nil), http.StatusNotFound)
	status(t, s.post("/watches/packages/99/dismiss", nil), http.StatusNotFound)
}

func TestWatchesPageListsAndDismissesBirthdays(t *testing.T) {
	dir := t.TempDir()
	cards := filepath.Join(dir, "cards")
	if err := os.Mkdir(cards, 0o755); err != nil {
		t.Fatal(err)
	}
	zone, err := config.Location(config.Map(nil))
	if err != nil {
		t.Fatal(err)
	}
	today := time.Now().In(zone)
	card := fmt.Sprintf("BEGIN:VCARD\nVERSION:3.0\nFN:Ada Lovelace\nBDAY:--%02d%02d\nEND:VCARD\n", int(today.Month()), today.Day())
	if err := os.WriteFile(filepath.Join(cards, "a.vcf"), []byte(card), 0o644); err != nil {
		t.Fatal(err)
	}
	s := newSite(t, func(env map[string]string) { env["ATRIUM_CONTACTS_DIR"] = cards })
	s.clock = float64(time.Now().Unix())
	page := status(t, s.get("/watches"), http.StatusOK)
	mustContain(t, page, "Ada Lovelace", "(today)", "(1)")
	mustNotContain(t, page, "Address book not configured")
	key := fmt.Sprintf("ada lovelace|birthday|%02d-%02d", int(today.Month()), today.Day())
	date := today.Format("2006-01-02")
	body := status(t, s.post("/watches/birthdays/dismiss", form("key", key, "date", date)), http.StatusOK)
	mustContain(t, body, "Hidden until next year.", "Nothing coming up")
	mustNotContain(t, body, "Ada Lovelace")
	bad := s.post("/watches/birthdays/dismiss", form("key", key, "date", "nope"))
	mustContain(t, status(t, bad, http.StatusUnprocessableEntity), "Date must be YYYY-MM-DD.")
	missing := status(t, s.post("/watches/birthdays/dismiss", nil), http.StatusUnprocessableEntity)
	if errs := jsonOf(t, missing)["detail"].([]any); len(errs) != 2 {
		t.Errorf("detail = %v", errs)
	}
}

func TestWatchesWithAnEmptyAddressBook(t *testing.T) {
	cards := t.TempDir()
	s := newSite(t, func(env map[string]string) { env["ATRIUM_CONTACTS_DIR"] = cards })
	mustContain(t, status(t, s.get("/watches"), http.StatusOK), "Nothing coming up", "(0)")
}

func TestEmptyFormFieldsAreAbsent(t *testing.T) {
	s := newSite(t)
	s.loop("a", "owe_reply", 2, "Dinner?", "pat@person.test", 1)
	target := fmt.Sprintf("/loops/%d/snooze", s.loopID("a"))
	mustContain(t, status(t, s.post(target, form("days", "")), http.StatusOK), fmt.Sprintf("Hidden for %d days.", openloops.SnoozeDefaultDays))
	status(t, s.post("/loops/dismiss-all", form("state", "")), http.StatusUnprocessableEntity)
}

func TestSnoozeClampsHugeDays(t *testing.T) {
	s := newSite(t)
	s.loop("a", "owe_reply", 2, "Dinner?", "pat@person.test", 1)
	target := fmt.Sprintf("/loops/%d/snooze", s.loopID("a"))
	huge := "99999999999999999999999999"
	mustContain(t, status(t, s.post(target, form("days", huge)), http.StatusOK), "Hidden for 60 days.")
	mustContain(t, status(t, s.post(target, form("days", "-"+huge)), http.StatusOK), "Hidden for 1 day.")
}

func TestPathAndBodyErrorsAreReportedTogether(t *testing.T) {
	s := newSite(t)
	count := func(rec interface{ String() string }) int {
		return len(jsonOf(t, rec.String())["detail"].([]any))
	}
	snooze := s.post("/loops/x/snooze", form("days", "soon"))
	if status(t, snooze, http.StatusUnprocessableEntity); count(snooze.Body) != 2 {
		t.Errorf("snooze detail = %s", snooze.Body)
	}
	update := s.post("/drafts/x", form("to", "a"))
	if status(t, update, http.StatusUnprocessableEntity); count(update.Body) != 2 {
		t.Errorf("update detail = %s", update.Body)
	}
}

func TestKindFilterAcceptsCalendarReminderAndWikiPlan(t *testing.T) {
	s := newSite(t)
	for _, kind := range []string{"calendar_event", "reminder", "notion_wiki_plan"} {
		status(t, s.get("/proposals?kind="+kind), http.StatusOK)
	}
}

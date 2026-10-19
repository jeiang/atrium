package birthdays_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/contacts"
	"github.com/jeiang/atrium/internal/db/dbtest"
	"github.com/jeiang/atrium/internal/httpx"
	"github.com/jeiang/atrium/internal/mail/birthdays"
)

func card(name string, kind, label string, month, day int, year *int) contacts.Card {
	return contacts.Card{Name: name, Dates: []contacts.Date{{Kind: kind, Label: label, Month: month, Day: day, Year: year}}}
}

func year(y int) *int { return &y }

func TestUpcomingWindowYearWrapAndLeapDay(t *testing.T) {
	cards := []contacts.Card{
		card("Ada", "birthday", "birthday", 10, 17, year(1990)),
		card("Bob", "anniversary", "Anniversary", 1, 2, nil),
		card("Leap", "birthday", "birthday", 2, 29, year(2000)),
		card("Far", "birthday", "birthday", 6, 1, nil),
	}
	found, err := birthdays.Upcoming(cards, birthdays.Date(2026, 10, 15), 14, nil)
	if err != nil || len(found) != 1 || found[0].Name != "Ada" || found[0].DaysUntil != 2 || found[0].Turning == nil || *found[0].Turning != 36 {
		t.Fatalf("Oct 15 = %+v, %v", found, err)
	}
	wrapped, _ := birthdays.Upcoming(cards, birthdays.Date(2026, 12, 25), 14, nil)
	if len(wrapped) != 1 || wrapped[0].Name != "Bob" || wrapped[0].Date != "2027-01-02" || wrapped[0].Turning != nil {
		t.Errorf("Dec 25 = %+v", wrapped)
	}
	leap, _ := birthdays.Upcoming(cards, birthdays.Date(2027, 2, 20), 14, nil)
	if len(leap) == 0 || leap[0].Date != "2027-02-28" {
		t.Errorf("Feb 20 2027 = %+v", leap)
	}
	if kept, _ := birthdays.Upcoming(cards, birthdays.Date(2028, 2, 20), 14, nil); len(kept) == 0 || kept[0].Date != "2028-02-29" {
		t.Errorf("leap year = %+v", kept)
	}
	if today, _ := birthdays.Upcoming(cards, birthdays.Date(2026, 10, 17), 0, nil); len(today) != 1 || today[0].DaysUntil != 0 {
		t.Errorf("today = %+v", today)
	}
}

func TestUpcomingSortsByDaysNameAndKind(t *testing.T) {
	cards := []contacts.Card{
		{Name: "zed", Dates: []contacts.Date{{Kind: "birthday", Label: "birthday", Month: 10, Day: 16}}},
		{Name: "Ada", Dates: []contacts.Date{{Kind: "birthday", Label: "birthday", Month: 10, Day: 16}, {Kind: "anniversary", Label: "Anniversary", Month: 10, Day: 16}}},
		{Name: "Soon", Dates: []contacts.Date{{Kind: "birthday", Label: "birthday", Month: 10, Day: 15}}},
	}
	found, err := birthdays.Upcoming(cards, birthdays.Date(2026, 10, 15), 14, nil)
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, i := range found {
		order = append(order, i.Name+"/"+i.Kind)
	}
	want := []string{"Soon/birthday", "Ada/anniversary", "Ada/birthday", "zed/birthday"}
	if len(order) != len(want) {
		t.Fatalf("order = %v", order)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("order = %v, want %v", order, want)
		}
	}
	if found[0].Emails == nil || found[0].Key != "soon|birthday|10-15" {
		t.Errorf("item = %+v", found[0])
	}
}

func TestBirthdayDismissalHidesOnlyThatOccurrence(t *testing.T) {
	d := dbtest.Mail(t)
	ctx := t.Context()
	cards := []contacts.Card{card("Ada", "birthday", "birthday", 10, 17, nil)}
	first, _ := birthdays.Upcoming(cards, birthdays.Date(2026, 10, 15), 14, nil)
	if err := birthdays.Dismiss(ctx, d.Write, first[0].Key, first[0].Date, 1); err != nil {
		t.Fatal(err)
	}
	hidden, err := birthdays.DismissedKeys(ctx, d.Read)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := birthdays.Upcoming(cards, birthdays.Date(2026, 10, 15), 14, hidden); len(got) != 0 {
		t.Errorf("dismissed occurrence listed: %+v", got)
	}
	if got, _ := birthdays.Upcoming(cards, birthdays.Date(2027, 10, 15), 14, hidden); len(got) != 1 || got[0].Date != "2027-10-17" {
		t.Errorf("next year = %+v", got)
	}
	if err := birthdays.Dismiss(ctx, d.Write, first[0].Key, "2027-10-17", 2); err != nil {
		t.Fatal(err)
	}
	var occurrence string
	if err := d.Read.QueryRow("SELECT occurrence FROM watch_dismissed").Scan(&occurrence); err != nil || occurrence != "2027-10-17" {
		t.Errorf("a second dismissal must replace the first: %q, %v", occurrence, err)
	}
	for _, bad := range []string{"nope", "2026-10-17T09:00", ""} {
		if err := birthdays.Dismiss(ctx, d.Write, "k", bad, 3); err == nil || !errors.Is(err, httpx.ErrValue) {
			t.Errorf("Dismiss(%q) = %v, want ValueError", bad, err)
		}
	}
}

func TestUpcomingFor(t *testing.T) {
	d := dbtest.Mail(t)
	ctx := t.Context()
	if items, ok, err := birthdays.UpcomingFor(ctx, d.Read, config.Config{}, birthdays.Date(2026, 10, 15), 14); items != nil || ok || err != nil {
		t.Errorf("no contacts dir = %v, %v, %v; want none", items, ok, err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.vcf"), []byte("BEGIN:VCARD\nVERSION:3.0\nFN:Ada\nBDAY:--1017\nEND:VCARD\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	items, ok, err := birthdays.UpcomingFor(ctx, d.Read, config.Config{ContactsDir: dir}, birthdays.Date(2026, 10, 15), 14)
	if err != nil || !ok || len(items) != 1 || items[0].Name != "Ada" || items[0].DaysUntil != 2 {
		t.Errorf("UpcomingFor = %+v, %v, %v", items, ok, err)
	}
	if items, _, _ := birthdays.UpcomingFor(ctx, d.Read, config.Config{ContactsDir: dir}, birthdays.Date(2026, 10, 15), 0); len(items) != 0 {
		t.Errorf("days=0 = %+v", items)
	}
}

func TestOccurrence(t *testing.T) {
	today := birthdays.Date(2026, 12, 31)
	if when, err := birthdays.Occurrence(1, 1, today); err != nil || !when.Equal(time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("Occurrence = %v, %v", when, err)
	}
	if when, err := birthdays.Occurrence(2, 30, birthdays.Date(2026, 1, 1)); err != nil || when.Format("2006-01-02") != "2026-02-28" {
		t.Errorf("an impossible date falls on Feb 28: %v, %v", when, err)
	}
}

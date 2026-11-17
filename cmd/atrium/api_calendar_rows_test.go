package main

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/apps/calendar/caldavtest"
)

const (
	standupICS = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\nBEGIN:VEVENT\r\nUID:standup\r\nDTSTAMP:20261001T000000Z\r\nDTSTART:20261102T140000Z\r\nDTEND:20261102T143000Z\r\nRRULE:FREQ=WEEKLY;COUNT=4\r\nSUMMARY:Standup\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	holidayICS = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\nBEGIN:VEVENT\r\nUID:holiday\r\nDTSTAMP:20261001T000000Z\r\nDTSTART;VALUE=DATE:20261103\r\nDTEND;VALUE=DATE:20261104\r\nSUMMARY:Holiday\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
)

func TestCalendarListRowsCarryTheWriteTargetAndRevision(t *testing.T) {
	_, srv := davFixture(t)
	seedSettings(t, map[string]string{"tz": "America/New_York"})
	srv.SetObject(caldavtest.CalendarPath(0)+"dentist.ics", strings.Replace(dentistICS, "SUMMARY:Dentist", "SUMMARY:Dentist\r\nDESCRIPTION:Bring the card", 1))
	srv.SetObject(caldavtest.CalendarPath(0)+"standup.ics", standupICS)
	srv.SetObject(caldavtest.CalendarPath(0)+"holiday.ics", holidayICS)
	token := localAPIToken(t, "api")
	base, _, stop, err := startServeAppArgs(t, fixtureFor(t).app, &lockedBuffer{}, "--auth", "local")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	s := &apiSite{t: t, base: base}
	browser := newWebClient(t, s.base, nil)
	browser.session("local")
	zone, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	days := func() []string {
		return []string{time.Now().In(zone).Format(time.DateOnly), time.Now().In(zone).Add(-time.Minute).Format(time.DateOnly)}
	}
	today := func(got any) bool {
		for _, d := range days() {
			if got == d {
				return true
			}
		}
		return false
	}

	if got := apiData(t, s.do(http.MethodPost, "/api/v1/calendar/sync", token, nil), http.StatusOK); number(t, got["written"]) != 3 {
		t.Fatalf("calendar sync: %v", got)
	}
	listing := apiData(t, s.do(http.MethodGet, "/api/v1/calendar/events?from=2026-11-01T00:00:00Z&to=2026-11-10T00:00:00Z", token, nil), http.StatusOK)
	if listing["timezone"] != "America/New_York" || !today(listing["today"]) {
		t.Fatalf("listing date and zone: today %v timezone %v, want one of %v", listing["today"], listing["timezone"], days())
	}
	detail := func(uid string) string {
		t.Helper()
		return apiData(t, s.do(http.MethodGet, "/api/v1/calendar/events/icloud/"+uid, token, nil), http.StatusOK)["revision"].(string)
	}
	rows := map[string][]map[string]any{}
	for _, raw := range listing["events"].([]any) {
		row := raw.(map[string]any)
		if row["calendar"] != "cal0" || row["target"] != "icloud" || row["read_only"] == true {
			t.Fatalf("iCloud row without its write target: %v", row)
		}
		rows[row["uid"].(string)] = append(rows[row["uid"].(string)], row)
	}
	dentist, standup, holiday := rows["dentist"], rows["standup"], rows["holiday"]
	if len(dentist) != 1 || dentist[0]["notes"] != "Bring the card" || dentist[0]["all_day"] == true || dentist[0]["recurring"] == true || dentist[0]["revision"] != detail("dentist") {
		t.Fatalf("dentist row: %v", dentist)
	}
	if len(standup) != 2 || standup[0]["recurring"] != true || standup[0]["all_day"] == true || standup[0]["revision"] != detail("standup") || standup[1]["revision"] != standup[0]["revision"] {
		t.Fatalf("standup rows: %v", standup)
	}
	if len(holiday) != 1 || holiday[0]["all_day"] != true || holiday[0]["start"] != "2026-11-03" || holiday[0]["recurring"] == true || holiday[0]["revision"] != detail("holiday") {
		t.Fatalf("holiday row: %v", holiday)
	}

	read := s.do(http.MethodGet, "/api/v1/calendar/calendars", token, nil)
	calendars := apiData(t, read, http.StatusOK)
	if web := browser.do(http.MethodGet, "/web/v1/calendar/calendars", nil); web.status != http.StatusOK || web.json["data"].(map[string]any)["timezone"] != "America/New_York" {
		t.Fatalf("web prefix calendars: %d %s", web.status, web.body)
	}
	if tag := read.header.Get("ETag"); tag == "" || s.do(http.MethodGet, "/api/v1/calendar/calendars", token, nil, "If-None-Match", tag).status != http.StatusNotModified {
		t.Fatalf("calendars read is not conditional: ETag %q", tag)
	}
	if calendars["timezone"] != "America/New_York" || !today(calendars["today"]) {
		t.Fatalf("calendars date and zone: %v", calendars)
	}
	want := map[string][2]bool{"icloud": {false, true}, "google": {false, false}, "birthdays": {true, false}}
	listed := calendars["calendars"].([]any)
	if len(listed) != len(want) {
		t.Fatalf("calendars: %v", listed)
	}
	for _, raw := range listed {
		c := raw.(map[string]any)
		flags, ok := want[c["id"].(string)]
		if !ok || c["read_only"] != flags[0] || c["available"] != flags[1] || c["name"] == "" {
			t.Fatalf("calendar %v, want read_only and available %v", c, flags)
		}
	}
}

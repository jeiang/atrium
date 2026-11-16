package main

import (
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/calendar"
	"github.com/jeiang/atrium/internal/unit"
)

type agendaFixture struct {
	s     *apiSite
	token string
	today string
	date  string
}

func noonZone(now time.Time) *time.Location {
	offset := 12 - now.UTC().Hour()
	name := "Etc/GMT"
	switch {
	case offset > 0:
		name += "-" + strconv.Itoa(offset)
	case offset < 0:
		name += "+" + strconv.Itoa(-offset)
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return loc
}

func agendaSite(t *testing.T, flags map[string]string) agendaFixture {
	t.Helper()
	dir, _, f := moduleFixture(t, flags)
	loc := noonZone(time.Now())
	seedSettings(t, map[string]string{"tz": loc.String()})
	now := time.Now().In(loc)
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	at := func(h, m int) string {
		return midnight.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute).UTC().Format("20060102T150405Z")
	}
	event := func(uid, title, location, start, end string) string {
		return "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\nBEGIN:VEVENT\r\nUID:" + uid + "\r\nDTSTAMP:20261001T000000Z\r\n" +
			start + "\r\n" + end + "\r\nSUMMARY:" + title + "\r\nLOCATION:" + location + "\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	}
	mirror := filepath.Join(dir, "calendars")
	setEnv(t, settingEnv(calendar.CalendarDirKey), mirror)
	if err := os.MkdirAll(filepath.Join(mirror, "home"), 0o750); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"overnight": event("overnight", "Night shift", "", "DTSTART:"+at(-1, 0), "DTEND:"+at(1, 0)),
		"gym":       event("gym", "Gym", "Room 1", "DTSTART:"+at(9, 0), "DTEND:"+at(10, 0)),
		"standup":   event("standup", "Standup", "", "DTSTART:"+at(11, 30), "DTEND:"+at(13, 30)),
		"holiday":   event("holiday", "Holiday", "", "DTSTART;VALUE=DATE:"+midnight.Format("20060102"), "DTEND;VALUE=DATE:"+midnight.AddDate(0, 0, 1).Format("20060102")),
		"beta":      event("beta", "Beta review", "", "DTSTART:"+at(15, 0), "DTEND:"+at(16, 0)),
		"alpha":     event("alpha", "Alpha review", "Clinic", "DTSTART:"+at(15, 0), "DTEND:"+at(16, 30)),
		"odd":       event("odd uid", "Odd", "", "DTSTART:"+at(18, 0), "DTEND:"+at(18, 30)),
		"tomorrow":  event("tomorrow", "Tomorrow", "", "DTSTART:"+at(33, 0), "DTEND:"+at(34, 0)),
	} {
		if err := os.WriteFile(filepath.Join(mirror, "home", name+".ics"), []byte(body), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	token := localAPIToken(t, "agenda")
	base, _, stop, err := startServeAppArgs(t, f.app, &lockedBuffer{}, "--auth", "local")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	return agendaFixture{s: &apiSite{t: t, base: base}, token: token, today: midnight.Format(time.DateOnly), date: midnight.Format("Monday, January 2")}
}

func TestHomeAgendaReadsTheDayFromCalendar(t *testing.T) {
	t.Parallel()
	a := agendaSite(t, flagsExcept("", ""))
	agenda := a.s.do(http.MethodGet, "/api/v1/agenda", a.token, nil)
	standup := `{"time":"11:30","title":"Standup","meta":"2 h","state":"now","ask":"event:TODAY:standup"}`
	want := strings.ReplaceAll(`{"agenda":{"events":[`+
		`{"time":"23:00","title":"Night shift","meta":"2 h","state":"past","ask":"event:TODAY:overnight"},`+
		`{"time":"all day","title":"Holiday","meta":"","state":"","ask":"event:TODAY:holiday"},`+
		`{"time":"09:00","title":"Gym","meta":"Room 1 · 1 h","state":"past","ask":"event:TODAY:gym"},`+
		standup+`,`+
		`{"time":"15:00","title":"Alpha review","meta":"Clinic · 1 h 30 min","state":"","ask":"event:TODAY:alpha"},`+
		`{"time":"15:00","title":"Beta review","meta":"1 h","state":"","ask":"event:TODAY:beta"},`+
		`{"time":"18:00","title":"Odd","meta":"30 min","state":"","ask":""}`+
		`],"next":`+standup+`}}`, "TODAY", a.today)
	if agenda.status != http.StatusOK || strings.TrimSpace(agenda.body) != want {
		t.Errorf("agenda = %d:\n%s\nwant:\n%s", agenda.status, agenda.body, want)
	}
	home := a.s.do(http.MethodGet, "/api/v1/home", a.token, nil)
	if home.status != http.StatusOK || home.json["agenda"] != true || home.json["date"] != a.date {
		t.Errorf("home = %d: %.400s", home.status, home.body)
	}
	t.Logf("home body: %s", home.body)
	if about := a.s.do(http.MethodGet, "/api/v1/home/about?ref=event:"+a.today+":alpha", a.token, nil); !strings.Contains(about.body, `"label":"Alpha review"`) {
		t.Errorf("an agenda ref did not resolve: %d %s", about.status, about.body)
	}
}

func TestHomeAgendaWithCalendarOff(t *testing.T) {
	t.Parallel()
	a := agendaSite(t, flagsExcept(unit.Calendar, "false"))
	if home := a.s.do(http.MethodGet, "/api/v1/home", a.token, nil); home.status != http.StatusOK || home.json["agenda"] != false || home.json["date"] != a.date {
		t.Errorf("home with calendar off = %d: %.400s", home.status, home.body)
	}
	expectAPI(t, a.s.do(http.MethodGet, "/api/v1/agenda", a.token, nil), http.StatusNotFound, "not_found")
	if about := a.s.do(http.MethodGet, "/api/v1/home/about?ref=event:"+a.today+":alpha", a.token, nil); about.status != http.StatusOK || !strings.Contains(about.body, `"about":null`) {
		t.Errorf("an event ref resolved with calendar off: %d %s", about.status, about.body)
	}
	if today := a.s.do(http.MethodPost, "/api/v1/today", a.token, nil); today.status != http.StatusOK && today.status != http.StatusCreated {
		t.Errorf("today with calendar off = %d: %s", today.status, today.body)
	}
}

package main

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/contacts"
	"github.com/jeiang/atrium/internal/unit"
)

const birthdayUID = "birthday-0123456789abcdef"

var birthdayRef = regexp.MustCompile(`"ask":"(event:[0-9-]+:birthday-[0-9a-f]{16})"`)

func birthdayCard(uid, name, date string) string {
	return "BEGIN:VCARD\r\nVERSION:3.0\r\nUID:" + uid + "\r\nFN:" + name + "\r\n" + date + "\r\nEND:VCARD\r\n"
}

func birthdayContacts(t *testing.T, dir string, today time.Time) {
	t.Helper()
	root := filepath.Join(dir, "contacts")
	setEnv(t, settingEnv(contacts.DirKey), root)
	if err := os.MkdirAll(filepath.Join(root, "card"), 0o750); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"ana":  birthdayCard("ana-1", "Ana Example", "BDAY:1990"+today.Format("-01-02")),
		"ben":  birthdayCard("ben-1", "Ben Leap", "BDAY:1604-02-29"),
		"cleo": birthdayCard("cleo-1", "Cleo Soon", "BDAY:2000"+today.AddDate(0, 0, 3).Format("-01-02")),
		"dee":  birthdayCard("dee-1", "Dee Wed", "ANNIVERSARY:2001"+today.Format("-01-02")),
	} {
		if err := os.WriteFile(filepath.Join(root, "card", name+".vcf"), []byte(body), 0o640); err != nil {
			t.Fatal(err)
		}
	}
}

func birthdayToday() (time.Time, *time.Location) {
	loc := noonZone(time.Now())
	now := time.Now().In(loc)
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc), loc
}

func birthdayResult(t *testing.T, r anthropictest.Request, id string) (string, bool) {
	t.Helper()
	entry := toolResultFor(t, r, id)
	return fmt.Sprint(entry["content"]), entry["is_error"] == true
}

func birthdayServe(t *testing.T, f *fixture) (*apiSite, string) {
	t.Helper()
	token := localAPIToken(t, "birthdays")
	base, _, stop, err := startServeAppArgs(t, f.app, &lockedBuffer{}, "--auth", "local")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	return &apiSite{t: t, base: base}, token
}

func TestBirthdaysCalendarIsDerivedFromContactDates(t *testing.T) {
	t.Parallel()
	dir, api, f := moduleFixture(t, flagsExcept("", ""))
	today, loc := birthdayToday()
	seedSettings(t, map[string]string{"tz": loc.String()})
	birthdayContacts(t, dir, today)
	anaTitle := fmt.Sprintf("Birthday: Ana Example (turns %d)", today.Year()-1990)
	start := warm(t, api)
	tools := offeredTools(t, api, runApp(t, "agent", "run", "--text", "tools"))
	if slices.Contains(tools, "upcoming_birthdays") || !slices.Contains(tools, "calendar_list") {
		t.Fatalf("offered tools = %v", tools)
	}
	start = len(api.Requests())
	api.Push(
		anthropictest.Reply("tool_use", anthropictest.ToolUse("c1", "contacts_lookup", map[string]any{"query": "Ana", "include": []any{"dates"}})),
		anthropictest.Reply("tool_use", anthropictest.ToolUse("l1", "calendar_list", map[string]any{
			"start": today.Format(time.DateOnly), "end": today.AddDate(0, 0, 7).Format(time.DateOnly), "calendar": "birthdays",
		})),
		anthropictest.Reply("tool_use", anthropictest.ToolUse("u1", "calendar_update", map[string]any{
			"calendar": "icloud", "uid": birthdayUID, "title": "Moved", "start": today.Format(time.DateOnly), "all_day": true,
		})),
		anthropictest.Reply("tool_use", anthropictest.ToolUse("d1", "calendar_delete", map[string]any{"uid": birthdayUID})),
		anthropictest.Reply("end_turn", anthropictest.Text("done")),
	)
	if ran := runApp(t, "agent", "run", "--text", "birthdays"); ran.code != cli.ExitOK {
		t.Fatalf("agent run: %d %s", ran.code, ran.stderr)
	}
	requests := api.Requests()[start:]
	if len(requests) != 5 {
		t.Fatalf("%d model requests, want 5", len(requests))
	}
	last := requests[4]
	if text, failed := birthdayResult(t, last, "c1"); failed || !strings.Contains(text, `"kind":"birthday"`) || !strings.Contains(text, `"year":1990`) {
		t.Errorf("contacts_lookup dates = %s", text)
	}
	text, failed := birthdayResult(t, last, "l1")
	if failed || !strings.Contains(text, anaTitle) || !strings.Contains(text, "Birthday: Cleo Soon (turns") ||
		!strings.Contains(text, `"calendar":"birthdays"`) || strings.Contains(text, "Dee Wed") {
		t.Errorf("calendar_list birthdays = %s", text)
	}
	for _, id := range []string{"u1", "d1"} {
		if text, failed := birthdayResult(t, last, id); !failed || !strings.Contains(text, "read-only") {
			t.Errorf("%s on a birthday = %v %s", id, failed, text)
		}
	}

	s, token := birthdayServe(t, f)
	window := func(from, to string) apiReply {
		return s.do(http.MethodGet, "/api/v1/calendar/events?calendar=birthdays&from="+from+"&to="+to, token, nil)
	}
	week := window(today.Format(time.DateOnly), today.AddDate(0, 0, 7).Format(time.DateOnly))
	if week.status != http.StatusOK || !strings.Contains(week.body, `"title":"`+anaTitle+`"`) || !strings.Contains(week.body, "Cleo Soon") || strings.Contains(week.body, "Dee Wed") {
		t.Errorf("birthdays week = %d %s", week.status, week.body)
	}
	if plain := window("2027-02-27", "2027-03-02"); !strings.Contains(plain.body, `"title":"Birthday: Ben Leap"`) || !strings.Contains(plain.body, "2027-02-28") {
		t.Errorf("Feb 29 in a common year = %d %s", plain.status, plain.body)
	}
	if leap := window("2028-02-27", "2028-03-02"); !strings.Contains(leap.body, `"title":"Birthday: Ben Leap"`) || !strings.Contains(leap.body, "2028-02-29") {
		t.Errorf("Feb 29 in a leap year = %d %s", leap.status, leap.body)
	}
	agenda := s.do(http.MethodGet, "/api/v1/agenda", token, nil)
	if agenda.status != http.StatusOK || !strings.Contains(agenda.body, `{"time":"all day","title":"`+anaTitle+`","meta":"","state":"","ask":"event:`+today.Format(time.DateOnly)+`:birthday-`) {
		t.Fatalf("agenda = %d %s", agenda.status, agenda.body)
	}
	if home := s.do(http.MethodGet, "/api/v1/home", token, nil); home.status != http.StatusOK || home.json["agenda"] != true {
		t.Errorf("home = %d %.400s", home.status, home.body)
	}
	ref := birthdayRef.FindStringSubmatch(agenda.body)
	if ref == nil {
		t.Fatalf("no birthday ref in %s", agenda.body)
	}
	if about := s.do(http.MethodGet, "/api/v1/home/about?ref="+url.QueryEscape(ref[1]), token, nil); !strings.Contains(about.body, `"label":"`+anaTitle+`"`) {
		t.Errorf("birthday ref = %d %s", about.status, about.body)
	}
	if gone := s.do(http.MethodGet, "/api/v1/mail/birthdays", token, nil); gone.status != http.StatusNotFound {
		t.Errorf("GET /api/v1/mail/birthdays = %d %s", gone.status, gone.body)
	}
}

func TestBirthdaysLeaveTodayWithCalendarOff(t *testing.T) {
	t.Parallel()
	dir, api, f := moduleFixture(t, flagsExcept(unit.Calendar, "false"))
	today, loc := birthdayToday()
	seedSettings(t, map[string]string{"tz": loc.String()})
	birthdayContacts(t, dir, today)
	start := warm(t, api)
	api.Push(
		anthropictest.Reply("tool_use", anthropictest.ToolUse("c1", "contacts_lookup", map[string]any{"query": "Ana", "include": []any{"dates"}})),
		anthropictest.Reply("end_turn", anthropictest.Text("done")),
	)
	if ran := runApp(t, "agent", "run", "--text", "birthday"); ran.code != cli.ExitOK {
		t.Fatalf("agent run: %d %s", ran.code, ran.stderr)
	}
	requests := api.Requests()[start:]
	if text, failed := birthdayResult(t, requests[len(requests)-1], "c1"); failed || !strings.Contains(text, `"kind":"birthday"`) {
		t.Errorf("contacts_lookup dates with calendar off = %s", text)
	}
	s, token := birthdayServe(t, f)
	if home := s.do(http.MethodGet, "/api/v1/home", token, nil); home.status != http.StatusOK || home.json["agenda"] != false {
		t.Errorf("home with calendar off = %d %.400s", home.status, home.body)
	}
	expectAPI(t, s.do(http.MethodGet, "/api/v1/agenda", token, nil), http.StatusNotFound, "not_found")
	if events := s.do(http.MethodGet, "/api/v1/calendar/events?calendar=birthdays", token, nil); events.status != http.StatusNotFound {
		t.Errorf("calendar events with calendar off = %d %s", events.status, events.body)
	}
}

func TestWeeklyReviewReadsBirthdaysThroughCalendarList(t *testing.T) {
	t.Parallel()
	today, _ := birthdayToday()
	d, api, f, before := startWeeklyReview(t,
		anthropictest.Reply("tool_use", anthropictest.ToolUse("w1", "calendar_list", map[string]any{
			"start": today.AddDate(0, 0, -1).Format(time.DateOnly), "end": today.AddDate(0, 0, 8).Format(time.DateOnly),
		})),
		asstReply("reviewed"),
	)
	birthdayContacts(t, f.dir, today)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan appRun, 1)
	go func() {
		done <- runAppContext(t, f, ctx, "serve", "--host", "127.0.0.1", "--port", "0", "--auth", "local")
	}()
	waitUntil(t, 60*time.Second, "the weekly review", schedulerTerminal(d, "weekly-review"))
	cancel()
	<-done
	if status, text := schedulerStatus(t, d, "weekly-review"); status != "ok" {
		t.Fatalf("weekly review %s: %s", status, text)
	}
	requests := api.Requests()[before:]
	if len(requests) != 2 {
		t.Fatalf("%d weekly review requests, want 2", len(requests))
	}
	if !strings.Contains(fmt.Sprint(requests[0].JSON(t)), "calendar_list from today to 7 days ahead") {
		t.Error("the weekly review prompt does not name calendar_list")
	}
	if text, failed := birthdayResult(t, requests[1], "w1"); failed || !strings.Contains(text, "Birthday: Ana Example") {
		t.Errorf("weekly review calendar_list = %v %s", failed, text)
	}
}

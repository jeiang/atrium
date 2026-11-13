package main

import (
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jeiang/atrium/internal/calendar"
	"github.com/jeiang/atrium/internal/calendar/caldavtest"
)

func TestCalendarContactsAPI(t *testing.T) {
	_, srv := davFixture(t)
	target, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	var cardFails atomic.Bool
	proxy := httputil.NewSingleHostReverseProxy(target)
	cards := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cardFails.Load() {
			http.Error(w, "down", http.StatusInternalServerError)
			return
		}
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(cards.Close)
	setEnv(t, calendar.CardDAVEnv, cards.URL+"/")
	srv.SetObject(caldavtest.CalendarPath(0)+"dentist.ics", dentistICS)
	srv.SetObject(caldavtest.BookPath+"alice.vcf", strings.Replace(aliceCard, "END:VCARD", "TEL:+15550100\r\nEND:VCARD", 1))
	token := localAPIToken(t, "api")
	base, _, stop, err := startServeAppArgs(t, fixtureFor(t).app, &lockedBuffer{}, "--auth", "local")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	s := &apiSite{t: t, base: base}

	if got := apiData(t, s.do(http.MethodPost, "/api/v1/calendar/sync", token, nil), http.StatusOK); number(t, got["written"]) != 1 {
		t.Fatalf("calendar sync: %v", got)
	}
	if got := apiData(t, s.do(http.MethodPost, "/api/v1/contacts/sync", token, nil), http.StatusOK); number(t, got["written"]) != 1 {
		t.Fatalf("contacts sync: %v", got)
	}
	events := "/api/v1/calendar/events?from=2026-11-01T00:00:00Z&to=2026-11-03T00:00:00Z"
	tag := expectConditional(t, s, token, events)
	listed := apiData(t, s.do(http.MethodGet, events, token, nil), http.StatusOK)["events"].([]any)
	if len(listed) != 1 || listed[0].(map[string]any)["title"] != "Dentist" {
		t.Fatalf("events: %v", listed)
	}
	srv.SetObject(caldavtest.CalendarPath(0)+"haircut.ics", strings.NewReplacer("UID:dentist", "UID:haircut", "SUMMARY:Dentist", "SUMMARY:Haircut", "20261101T1", "20261102T1").Replace(dentistICS))
	apiData(t, s.do(http.MethodPost, "/api/v1/calendar/sync", token, nil), http.StatusOK)
	if listed := expectChanged(t, s, token, events, tag)["events"].([]any); len(listed) != 2 {
		t.Fatalf("events after the second sync: %v", listed)
	}

	found := apiData(t, s.do(http.MethodGet, "/api/v1/contacts?q=Alice&include=emails", token, nil), http.StatusOK)["results"].([]any)
	if len(found) != 1 {
		t.Fatalf("contacts: %v", found)
	}
	alice := found[0].(map[string]any)
	if emails, _ := alice["emails"].([]any); len(emails) != 1 || emails[0] != "alice@example.com" || alice["phones"] != nil {
		t.Fatalf("contact with emails only: %v", alice)
	}
	if bare := apiData(t, s.do(http.MethodGet, "/api/v1/contacts?q=Alice", token, nil), http.StatusOK)["results"].([]any); bare[0].(map[string]any)["emails"] != nil {
		t.Fatalf("contact without include: %v", bare)
	}
	expectField(t, s.do(http.MethodGet, "/api/v1/contacts?q=Alice&limit=0", token, nil), "limit")
	expectAPI(t, s.do(http.MethodGet, "/api/v1/contacts?q=Alice&include=bogus", token, nil), http.StatusUnprocessableEntity, "invalid")

	cardFails.Store(true)
	if failed := s.do(http.MethodPost, "/api/v1/contacts/sync", token, nil); failed.status < 400 {
		t.Fatalf("contacts sync against a failing CardDAV: %d %s", failed.status, failed.body)
	}
	apiData(t, s.do(http.MethodPost, "/api/v1/calendar/sync", token, nil), http.StatusOK)
	if again := apiData(t, s.do(http.MethodGet, "/api/v1/contacts?q=Alice&include=emails", token, nil), http.StatusOK)["results"].([]any); len(again) != 1 {
		t.Fatalf("a failed contacts sync dropped the mirror: %v", again)
	}
}

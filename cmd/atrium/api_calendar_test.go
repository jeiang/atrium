package main

import (
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jeiang/atrium/internal/apps/calendar/caldavtest"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/davmirror"
)

func TestCalendarContactsAPI(t *testing.T) {
	t.Parallel()
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
	setEnv(t, davmirror.CardDAVEnv, cards.URL+"/")
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

type davHold struct {
	mu      sync.Mutex
	entered chan struct{}
	release chan struct{}
}

func (h *davHold) arm() {
	h.mu.Lock()
	h.entered, h.release = make(chan struct{}), make(chan struct{})
	h.mu.Unlock()
}

func (h *davHold) wait() <-chan struct{} {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.entered
}

func (h *davHold) open() {
	h.mu.Lock()
	release := h.release
	h.entered, h.release = nil, nil
	h.mu.Unlock()
	close(release)
}

func (h *davHold) block() {
	h.mu.Lock()
	entered, release := h.entered, h.release
	if entered != nil {
		select {
		case <-entered:
		default:
			close(entered)
		}
	}
	h.mu.Unlock()
	if release != nil {
		<-release
	}
}

func TestCalendarContactsSyncAPITakesTheJobLock(t *testing.T) {
	t.Parallel()
	_, srv := davFixture(t)
	target, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	hold := &davHold{}
	proxy := httputil.NewSingleHostReverseProxy(target)
	dav := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hold.block()
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(dav.Close)
	setEnv(t, davmirror.CalDAVEnv, dav.URL+"/")
	setEnv(t, davmirror.CardDAVEnv, dav.URL+"/")
	srv.SetObject(caldavtest.CalendarPath(0)+"dentist.ics", dentistICS)
	srv.SetObject(caldavtest.BookPath+"alice.vcf", aliceCard)
	token := localAPIToken(t, "api")
	base, _, stop, err := startServeAppArgs(t, fixtureFor(t).app, &lockedBuffer{}, "--auth", "local")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	s := &apiSite{t: t, base: base}

	for _, kind := range []string{"calendar", "contacts"} {
		hold.arm()
		entered := hold.wait()
		first := make(chan apiReply, 1)
		go func() { first <- s.do(http.MethodPost, "/api/v1/"+kind+"/sync", token, nil) }()
		<-entered
		expectAPI(t, s.do(http.MethodPost, "/api/v1/"+kind+"/sync", token, nil), http.StatusConflict, "conflict")
		if got := runApp(t, kind, "sync"); got.code != cli.ExitError || !strings.Contains(got.stderr, "held by another invocation") {
			t.Fatalf("%s sync CLI beside the API sync = (%d, %q), want the held-lock refusal", kind, got.code, got.stderr)
		}
		hold.open()
		if got := apiData(t, <-first, http.StatusOK); number(t, got["written"]) != 1 {
			t.Fatalf("%s sync: %v", kind, got)
		}
	}
}

func TestCalendarCandidatesAPIAndCLIAreGone(t *testing.T) {
	_, api, _ := moduleFixture(t, flagsExcept("", ""))
	token := localAPIToken(t, "api")
	base, _, stop, err := startServeAppArgs(t, fixtureFor(t).app, &lockedBuffer{}, "--auth", "local")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	s := &apiSite{t: t, base: base}
	if got := s.do(http.MethodPost, "/api/v1/calendar/candidates", token, nil); got.status != http.StatusNotFound && got.status != http.StatusMethodNotAllowed {
		t.Fatalf("POST /api/v1/calendar/candidates = %d, want 404 or 405: %.400s", got.status, got.body)
	}
	if got := runApp(t, "calendar", "candidates"); got.code == cli.ExitOK {
		t.Fatalf("calendar candidates CLI still runs: %q", got.stdout)
	}
	if got := len(api.Requests()); got != 0 {
		t.Fatalf("removed candidate routes sent %d model requests", got)
	}
}

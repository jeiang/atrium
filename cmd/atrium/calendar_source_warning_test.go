package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jeiang/atrium/internal/apps/calendar/caldavtest"
)

func calendarEventNotes(t *testing.T, googleConfig string, google http.HandlerFunc) ([]any, *httptest.Server, *atomic.Int32) {
	t.Helper()
	dir, dav := davFixture(t)
	var hits atomic.Int32
	gsrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		google(w, r)
	}))
	t.Cleanup(gsrv.Close)
	files := map[string]string{"client_secret": "secret\n", "refresh": "refresh-token\n"}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	setEnv(t, "ATRIUM_HOST_CONFIG", hostConfigEdit(t, dir, "host.toml", nil, `
[connections."google/default"]
kind = "google"
label = "Google"
config = `+googleConfig+`
capabilities = ["calendar"]
credential_files = { "oauth/client_secret" = "`+filepath.Join(dir, "client_secret")+`", "oauth/calendar/refresh" = "`+filepath.Join(dir, "refresh")+`" }
`))
	t.Setenv("ATRIUM_GOOGLE_TOKEN_URL", gsrv.URL+"/token")
	t.Setenv("ATRIUM_CALENDAR_GOOGLE_TOKEN_URL", gsrv.URL+"/token")
	setEnv(t, "ATRIUM_CALENDAR_GOOGLE_API_URL", gsrv.URL+"/calendar/v3/")
	dav.SetObject(caldavtest.CalendarPath(0)+"dentist.ics", dentistICS)
	token := localAPIToken(t, "api")
	base, _, stop, err := startServeAppArgs(t, fixtureFor(t).app, &lockedBuffer{}, "--auth", "local")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	s := &apiSite{t: t, base: base}
	apiData(t, s.do(http.MethodPost, "/api/v1/calendar/sync", token, nil), http.StatusOK)
	hits.Store(0)
	read := apiData(t, s.do(http.MethodGet, "/api/v1/calendar/events?from=2026-11-01T00:00:00Z&to=2026-11-03T00:00:00Z", token, nil), http.StatusOK)
	if listed, _ := read["events"].([]any); len(listed) != 1 {
		t.Fatalf("events: %v", read)
	}
	notes, ok := read["notes"].([]any)
	if !ok {
		t.Fatalf("notes: %v", read)
	}
	return notes, gsrv, &hits
}

func TestCalendarEventsSkipAnUnconfiguredGoogleSourceWithoutAWarning(t *testing.T) {
	notes, _, hits := calendarEventNotes(t, "{}", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unexpected", http.StatusTeapot)
	})
	if len(notes) != 0 {
		t.Fatalf("notes with Google unconfigured = %q, want none", notes)
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("Google got %d requests while unconfigured, want 0", n)
	}
}

func TestCalendarEventsNameAFailingGoogleSourceWithAPlainReason(t *testing.T) {
	notes, gsrv, _ := calendarEventNotes(t, `{ client_id = "client-id" }`, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"Token has been expired or revoked."}`))
	})
	if len(notes) != 1 {
		t.Fatalf("notes with a failing Google = %q, want one", notes)
	}
	note, _ := notes[0].(string)
	address := strings.TrimPrefix(gsrv.URL, "http://")
	if !strings.HasPrefix(note, "Google: ") || !strings.Contains(note, "invalid_grant") ||
		strings.Contains(note, address) || strings.Contains(note, "http") || strings.Contains(note, "?") {
		t.Fatalf("failing Google note = %q, want the source and a plain reason without the request address", note)
	}
}

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jeiang/atrium/internal/apps/calendar/caldavtest"
)

func calendarEffect(t *testing.T, s *apiSite, token string, got apiReply) string {
	t.Helper()
	effects, _ := got.json["effect_ids"].([]any)
	if len(effects) != 1 || got.json["undoable"] != true {
		t.Fatalf("calendar write is not reported undoable: %s", got.body)
	}
	return fmt.Sprint(number(t, effects[0]))
}

func calendarEffectState(t *testing.T, s *apiSite, token, id string) string {
	t.Helper()
	got := s.do(http.MethodGet, "/api/v1/effects/"+id, token, nil)
	if got.status != http.StatusOK {
		t.Fatalf("effect %s: %d %s", id, got.status, got.body)
	}
	data := got.json
	if nested, ok := data["data"].(map[string]any); ok {
		data = nested
	}
	if nested, ok := data["effect"].(map[string]any); ok {
		data = nested
	}
	if data["state"] == nil {
		t.Fatalf("effect %s has no state: %s", id, got.body)
	}
	return fmt.Sprint(data["state"])
}

func undoCalendarEffect(t *testing.T, s *apiSite, token, id string) {
	t.Helper()
	if undone := s.do(http.MethodPost, "/api/v1/effects/"+id+"/undo", token, nil); undone.status != http.StatusOK || undone.json["undone"] != true {
		t.Fatalf("undo of calendar effect %s: %d %s", id, undone.status, undone.body)
	}
}

func icloudObjectWith(srv *caldavtest.Server, fragment string) (string, bool) {
	for path, body := range srv.Objects() {
		if strings.Contains(body, fragment) {
			return path, true
		}
	}
	return "", false
}

func TestCalendarOperatorEventWritesICloud(t *testing.T) {
	t.Parallel()
	_, srv := davFixture(t)
	seedSettings(t, map[string]string{"tz": "America/New_York"})
	srv.SetObject(caldavtest.CalendarPath(0)+"dentist.ics", dentistICS)
	token := localAPIToken(t, "api")
	base, _, stop, err := startServeAppArgs(t, fixtureFor(t).app, &lockedBuffer{}, "--auth", "local")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	s := &apiSite{t: t, base: base}

	report, code := runCatalogOperations(t, "--json")
	if code != 0 {
		t.Fatalf("catalog operations --json exited %d", code)
	}
	for _, name := range []string{"calendar.event", "calendar.event-create", "calendar.event-update", "calendar.event-delete"} {
		row, ok := hasOperation(report.Operations, name)
		if !ok || len(row.Authority) != 1 || row.Authority[0] != "operator" {
			t.Fatalf("%s authority: %+v", name, row)
		}
		for _, b := range row.Bindings {
			if b.Kind != "api" {
				t.Fatalf("%s is bound outside the API: %+v", name, row.Bindings)
			}
		}
	}

	dentist := "/api/v1/calendar/events/icloud/dentist"
	read := s.do(http.MethodGet, dentist, token, nil)
	current := apiData(t, read, http.StatusOK)
	if current["title"] != "Dentist" || current["start"] != "2026-11-01T10:00:00-05:00" || read.header.Get("ETag") != `W/"`+current["revision"].(string)+`"` {
		t.Fatalf("event read: %v etag %q", current, read.header.Get("ETag"))
	}
	browser := newWebClient(t, s.base, nil)
	browser.session("local")
	if web := browser.do(http.MethodGet, "/web/v1/calendar/events/icloud/dentist", nil); web.status != http.StatusOK || web.json["data"].(map[string]any)["revision"] != current["revision"] {
		t.Fatalf("web prefix read: %d %s", web.status, web.body)
	}
	expectAPI(t, s.do(http.MethodGet, "/api/v1/calendar/events/icloud/nobody", token, nil), http.StatusNotFound, "not_found")

	change := map[string]any{"title": "Dentist checkup", "location": "Suite 4"}
	expectField(t, s.do(http.MethodPatch, dentist, token, change), "If-Match")
	expectAPI(t, s.do(http.MethodPatch, dentist, token, change, "If-Match", `W/"stale"`), http.StatusPreconditionFailed, "precondition_failed")
	if body := srv.Objects()[caldavtest.CalendarPath(0)+"dentist.ics"]; body != dentistICS {
		t.Fatalf("a stale edit wrote the event: %q", body)
	}
	edited := s.do(http.MethodPatch, dentist, token, change, "If-Match", read.header.Get("ETag"))
	after := apiData(t, edited, http.StatusOK)
	if after["title"] != "Dentist checkup" || after["location"] != "Suite 4" || after["start"] != current["start"] || after["revision"] == current["revision"] {
		t.Fatalf("edited event: %v", after)
	}
	editID := calendarEffect(t, s, token, edited)
	if body := srv.Objects()[caldavtest.CalendarPath(0)+"dentist.ics"]; !strings.Contains(body, "SUMMARY:Dentist checkup") || !strings.Contains(body, "SEQUENCE:1") {
		t.Fatalf("edited object: %q", body)
	}
	if state := calendarEffectState(t, s, token, editID); state != "done" {
		t.Fatalf("edit effect state %q", state)
	}
	undoCalendarEffect(t, s, token, editID)
	if restored := apiData(t, s.do(http.MethodGet, dentist, token, nil), http.StatusOK); restored["revision"] != current["revision"] || strings.Contains(srv.Objects()[caldavtest.CalendarPath(0)+"dentist.ics"], "Suite 4") {
		body := srv.Objects()[caldavtest.CalendarPath(0)+"dentist.ics"]
		t.Fatalf("undo of the edit did not restore the event: %q", body)
	}

	created := browser.do(http.MethodPost, "/web/v1/calendar/events", map[string]any{
		"calendar": "icloud", "title": "Piano lesson", "start": "2026-11-05T22:00:00Z", "end": "2026-11-05T23:00:00Z", "notes": "Bring the book"})
	made := apiData(t, created, http.StatusCreated)
	uid, _ := made["uid"].(string)
	createID := calendarEffect(t, s, token, created)
	if _, ok := icloudObjectWith(srv, "SUMMARY:Piano lesson"); uid == "" || !ok || made["revision"] == "" {
		t.Fatalf("created event: %v objects %v", made, srv.Objects())
	}
	if fresh := apiData(t, s.do(http.MethodGet, "/api/v1/calendar/events/icloud/"+uid, token, nil), http.StatusOK); fresh["revision"] != made["revision"] {
		t.Fatalf("revision after create %v, read gives %v", made["revision"], fresh["revision"])
	}
	undoCalendarEffect(t, s, token, createID)
	if _, ok := icloudObjectWith(srv, "SUMMARY:Piano lesson"); ok {
		t.Fatalf("undo of the create left the event: %v", srv.Objects())
	}

	expectAPI(t, s.do(http.MethodDelete, dentist, token, nil, "If-Match", `W/"stale"`), http.StatusPreconditionFailed, "precondition_failed")
	kept := srv.Objects()[caldavtest.CalendarPath(0)+"dentist.ics"]
	removed := s.do(http.MethodDelete, dentist, token, nil, "If-Match", read.header.Get("ETag"))
	if got := apiData(t, removed, http.StatusOK); got["deleted"] != "dentist" {
		t.Fatalf("delete: %s", removed.body)
	}
	deleteID := calendarEffect(t, s, token, removed)
	if _, ok := srv.Objects()[caldavtest.CalendarPath(0)+"dentist.ics"]; ok {
		t.Fatal("delete left the event")
	}
	undoCalendarEffect(t, s, token, deleteID)
	if body := srv.Objects()[caldavtest.CalendarPath(0)+"dentist.ics"]; body != kept {
		t.Fatalf("undo of the delete did not put the event back: %q", body)
	}

	expectAPI(t, s.do(http.MethodPost, "/api/v1/calendar/events", token, map[string]any{"calendar": "birthdays", "title": "Party", "start": "2026-11-05T22:00:00Z"}), http.StatusConflict, "refused")
	expectAPI(t, s.do(http.MethodDelete, "/api/v1/calendar/events/icloud/birthday-alice", token, nil), http.StatusConflict, "refused")

	reread := s.do(http.MethodGet, dentist, token, nil)
	apiData(t, reread, http.StatusOK)
	srv.FailPuts, srv.PutStatus = 1, http.StatusInternalServerError
	lost := s.do(http.MethodPatch, dentist, token, map[string]any{"title": "Dentist lost"}, "If-Match", reread.header.Get("ETag"))
	expectAPI(t, lost, http.StatusConflict, "effect_unknown")
	lostIDs, _ := lost.json["error"].(map[string]any)["effect_ids"].([]any)
	if len(lostIDs) != 1 {
		t.Fatalf("unknown outcome without its effect: %s", lost.body)
	}
	if state := calendarEffectState(t, s, token, fmt.Sprint(number(t, lostIDs[0]))); state != "unknown" {
		t.Fatalf("lost edit effect state %q", state)
	}
	attempts := srv.PutAttempts
	expectAPI(t, s.do(http.MethodPatch, dentist, token, map[string]any{"title": "Dentist again"}, "If-Match", reread.header.Get("ETag")), http.StatusConflict, "refused")
	if srv.PutAttempts != attempts {
		t.Fatalf("a write to an event with an unknown outcome reached the server: %d puts, was %d", srv.PutAttempts, attempts)
	}
}

type gcalFake struct {
	mu     sync.Mutex
	events map[string]map[string]any
	next   int
}

func (g *gcalFake) fail(w http.ResponseWriter, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": status, "message": http.StatusText(status)}})
}

func (g *gcalFake) handler(w http.ResponseWriter, r *http.Request) {
	if strings.HasSuffix(r.URL.Path, "/token") {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "access", "token_type": "Bearer", "expires_in": 3600})
		return
	}
	_, rest, ok := strings.Cut(r.URL.Path, "/calendars/primary/events")
	if !ok {
		g.fail(w, http.StatusNotFound)
		return
	}
	id := strings.TrimPrefix(rest, "/")
	g.mu.Lock()
	defer g.mu.Unlock()
	var body map[string]any
	if r.Method == http.MethodPost || r.Method == http.MethodPatch {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			g.fail(w, http.StatusBadRequest)
			return
		}
	}
	ev := g.events[id]
	switch {
	case r.Method == http.MethodGet && id == "":
		items := []map[string]any{}
		for _, e := range g.events {
			if e["status"] != "cancelled" {
				items = append(items, e)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"items": items})
		return
	case r.Method == http.MethodPost && id == "":
		if body["id"] == nil {
			g.next++
			body["id"] = fmt.Sprintf("g%d", g.next)
		}
		body["status"] = "confirmed"
		g.events[body["id"].(string)] = body
		ev = body
	case ev == nil:
		g.fail(w, http.StatusNotFound)
		return
	case r.Method == http.MethodPatch:
		for k, v := range body {
			ev[k] = v
		}
	case r.Method == http.MethodDelete:
		if ev["status"] == "cancelled" {
			g.fail(w, http.StatusGone)
			return
		}
		ev["status"] = "cancelled"
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(ev)
}

func (g *gcalFake) event(id string) map[string]any {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := map[string]any{}
	for k, v := range g.events[id] {
		out[k] = v
	}
	return out
}

func TestCalendarOperatorEventWritesGoogle(t *testing.T) {
	dir, _ := davFixture(t)
	seedSettings(t, map[string]string{"tz": "America/New_York"})
	google := &gcalFake{events: map[string]map[string]any{
		"standup": {"id": "standup", "status": "confirmed", "summary": "Standup", "location": "Room 2",
			"start": map[string]any{"dateTime": "2026-11-03T14:00:00Z"}, "end": map[string]any{"dateTime": "2026-11-03T14:30:00Z"}},
	}}
	gsrv := httptest.NewServer(http.HandlerFunc(google.handler))
	t.Cleanup(gsrv.Close)
	files := map[string]string{"client_secret": "secret\n", "refresh": "refresh-token\n"}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	host := `
[connections."google/default"]
kind = "google"
label = "Google"
config = { client_id = "client-id" }
capabilities = ["calendar"]
credential_files = { "oauth/client_secret" = "` + filepath.Join(dir, "client_secret") + `", "oauth/calendar/refresh" = "` + filepath.Join(dir, "refresh") + `" }
`
	if err := os.WriteFile(filepath.Join(dir, "host.toml"), []byte(host), 0o600); err != nil {
		t.Fatal(err)
	}
	setEnv(t, "ATRIUM_HOST_CONFIG", filepath.Join(dir, "host.toml"))
	t.Setenv("ATRIUM_GOOGLE_TOKEN_URL", gsrv.URL+"/token")
	t.Setenv("ATRIUM_CALENDAR_GOOGLE_TOKEN_URL", gsrv.URL+"/token")
	setEnv(t, "ATRIUM_CALENDAR_GOOGLE_API_URL", gsrv.URL+"/calendar/v3/")
	token := localAPIToken(t, "api")
	base, _, stop, err := startServeAppArgs(t, fixtureFor(t).app, &lockedBuffer{}, "--auth", "local")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	s := &apiSite{t: t, base: base}

	standup := "/api/v1/calendar/events/google/standup"
	read := s.do(http.MethodGet, standup, token, nil)
	if current := apiData(t, read, http.StatusOK); current["title"] != "Standup" || current["start"] != "2026-11-03T09:00:00-05:00" {
		t.Fatalf("google read: %v", current)
	}
	expectAPI(t, s.do(http.MethodPatch, standup, token, map[string]any{"title": "Daily"}, "If-Match", `"stale"`), http.StatusPreconditionFailed, "precondition_failed")
	edited := s.do(http.MethodPatch, standup, token, map[string]any{"title": "Daily", "start": "2026-11-03T15:00:00Z", "end": "2026-11-03T15:30:00Z"}, "If-Match", read.header.Get("ETag"))
	if after := apiData(t, edited, http.StatusOK); after["title"] != "Daily" || after["location"] != "Room 2" {
		t.Fatalf("google edit: %v", after)
	}
	if ev := google.event("standup"); ev["summary"] != "Daily" {
		t.Fatalf("google event after edit: %v", ev)
	}
	undoCalendarEffect(t, s, token, calendarEffect(t, s, token, edited))
	if ev := google.event("standup"); ev["summary"] != "Standup" || ev["start"].(map[string]any)["dateTime"] != "2026-11-03T14:00:00Z" {
		t.Fatalf("google event after undoing the edit: %v", ev)
	}

	created := s.do(http.MethodPost, "/api/v1/calendar/events", token, map[string]any{"calendar": "google", "title": "Lunch", "start": "2026-11-04", "all_day": true})
	made := apiData(t, created, http.StatusCreated)
	uid, _ := made["uid"].(string)
	if ev := google.event(uid); ev["summary"] != "Lunch" || made["all_day"] != true {
		t.Fatalf("google create: %v event %v", made, ev)
	}
	undoCalendarEffect(t, s, token, calendarEffect(t, s, token, created))
	if ev := google.event(uid); ev["status"] != "cancelled" {
		t.Fatalf("google event after undoing the create: %v", ev)
	}

	removed := s.do(http.MethodDelete, standup, token, nil)
	apiData(t, removed, http.StatusOK)
	if ev := google.event("standup"); ev["status"] != "cancelled" {
		t.Fatalf("google delete: %v", ev)
	}
	expectAPI(t, s.do(http.MethodGet, standup, token, nil), http.StatusNotFound, "not_found")
	undoCalendarEffect(t, s, token, calendarEffect(t, s, token, removed))
	if ev := google.event("standup"); ev["status"] != "confirmed" || ev["summary"] != "Standup" {
		t.Fatalf("google event after undoing the delete: %v", ev)
	}
}

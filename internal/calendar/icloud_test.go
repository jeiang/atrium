package calendar

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/calendar/caldavtest"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/py"
)

func icloudEnv(extra map[string]string) config.Env {
	env := map[string]string{"ICLOUD_APPLE_ID": "me@icloud.example", "ICLOUD_APP_PASSWORD": "app-secret", "ATRIUM_TZ": "America/Port_of_Spain"}
	for k, v := range extra {
		env[k] = v
	}
	return config.Map(env)
}

func TestICloudCreateAndDelete(t *testing.T) {
	server := caldavtest.New(t, "me@icloud.example", "app-secret", "Work", "Home")
	t.Cleanup(UseEndpoints(Endpoints{CalDAV: server.URL + "/"}))
	ctx := context.Background()
	tz := zoneOf(t, "America/Port_of_Spain")
	spec := goldenSpec(t, "evt-1", py.Dict{{Key: "title", Value: "Lunch, with; friends"}, {Key: "start", Value: "2026-10-20T12:00"}, {Key: "location", Value: "Café"}})

	ref, err := CreateEvent(ctx, icloudEnv(map[string]string{"ATRIUM_ICLOUD_CALENDAR": " home "}), ICloud, spec, tz)
	if err != nil || ref.UID != "evt-1" {
		t.Fatalf("CreateEvent = %+v, %v", ref, err)
	}
	objects := server.Objects()
	body, ok := objects[caldavtest.CalendarPath(1)+"evt-1.ics"]
	if !ok {
		t.Fatalf("stored at %v", objects)
	}
	for _, line := range []string{"UID:evt-1", `SUMMARY:Lunch\, with\; friends`, "DTSTART:20261020T160000Z", "DTEND:20261020T170000Z", "LOCATION:Café"} {
		if !strings.Contains(body, line) {
			t.Errorf("body lacks %q:\n%s", line, body)
		}
	}
	if strings.Contains(body, "ATTENDEE") || strings.Contains(body, "ORGANIZER") {
		t.Errorf("attendees in body:\n%s", body)
	}

	if err := DeleteEvent(ctx, icloudEnv(map[string]string{"ATRIUM_ICLOUD_CALENDAR": "home"}), ICloud, "evt-1"); err != nil {
		t.Fatal(err)
	}
	if len(server.Objects()) != 0 {
		t.Errorf("not deleted: %v", server.Objects())
	}
	if err := DeleteEvent(ctx, icloudEnv(nil), ICloud, "evt-1"); err != nil {
		t.Errorf("deleting a missing event: %v", err)
	}
}

func TestICloudCalendarChoice(t *testing.T) {
	server := caldavtest.New(t, "me@icloud.example", "app-secret", "Work", "Home")
	t.Cleanup(UseEndpoints(Endpoints{CalDAV: server.URL + "/"}))
	tz := zoneOf(t, "America/Port_of_Spain")
	spec := goldenSpec(t, "evt-2", py.Dict{{Key: "title", Value: "A"}, {Key: "start", Value: "2026-10-20T12:00"}})
	_, err := CreateEvent(context.Background(), icloudEnv(map[string]string{"ATRIUM_ICLOUD_CALENDAR": "Nowhere"}), ICloud, spec, tz)
	if err == nil || err.Error() != "iCloud calendar 'nowhere' not found" {
		t.Errorf("err = %v", err)
	}
	if _, err := CreateEvent(context.Background(), icloudEnv(nil), ICloud, spec, tz); err != nil {
		t.Fatal(err)
	}
	if _, ok := server.Objects()[caldavtest.CalendarPath(0)+"evt-2.ics"]; !ok {
		t.Errorf("default calendar is the first event calendar: %v", server.Objects())
	}
	_, err = CreateEvent(context.Background(), config.Map(map[string]string{}), ICloud, spec, tz)
	var missing NotConfigured
	if err == nil || err.Error() != "ICLOUD_APPLE_ID, ICLOUD_APP_PASSWORD not set" || !errors.As(err, &missing) {
		t.Errorf("err = %v", err)
	}
}

func TestMirrorListing(t *testing.T) {
	processZone(t)
	root := t.TempDir()
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(home, 0o777); err != nil {
		t.Fatal(err)
	}
	ics, err := os.ReadFile(filepath.Join(corpusDir(), "calendar", "single_utc.ics"))
	if err != nil {
		t.Fatal(err)
	}
	for path, data := range map[string][]byte{
		filepath.Join(home, "a.ics"):       ics,
		filepath.Join(home, "displayname"): []byte("Personal\n"),
		filepath.Join(root, "broken.ics"):  []byte("not a calendar"),
		filepath.Join(root, "ignored.txt"): ics,
	} {
		if err := os.WriteFile(path, data, 0o666); err != nil {
			t.Fatal(err)
		}
	}
	env := config.Map(map[string]string{"ATRIUM_TZ": "America/Port_of_Spain", "ATRIUM_CALENDAR_DIR": root})
	listing, err := ListEvents(context.Background(), env, "2026-10-15", "2026-10-18", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(listing.Events) != 1 || listing.Events[0] != (Row{UID: "dentist-1", Calendar: "Personal", Start: "2026-10-16T09:00:00-04:00", End: "2026-10-16T10:00:00-04:00", Title: "Dentist", Location: "Main St"}) {
		t.Errorf("events = %+v", listing.Events)
	}
	wantNotes := "broken.ics: unreadable (ValueError)|google: GOOGLE_OAUTH_CLIENT_ID, GOOGLE_OAUTH_CLIENT_SECRET, GOOGLE_REFRESH_TOKEN not set"
	if got := strings.Join(listing.Notes, "|"); got != wantNotes {
		t.Errorf("notes %q, want %q", got, wantNotes)
	}
	named, err := ListEvents(context.Background(), env, "2026-10-15", "2026-10-18", "personal")
	if err != nil || len(named.Events) != 1 || len(named.Notes) != 0 {
		t.Errorf("named listing = %+v, %v", named, err)
	}
	other, err := ListEvents(context.Background(), env, "2026-10-15", "2026-10-18", "other")
	if err != nil || len(other.Events) != 0 {
		t.Errorf("other listing = %+v, %v", other, err)
	}
}

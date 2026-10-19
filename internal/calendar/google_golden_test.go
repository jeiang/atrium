package calendar

import (
	"context"
	"errors"
	"net/url"
	"testing"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/testutil/golden"
)

func normalizeQuery(raw string) string {
	values, err := url.ParseQuery(raw)
	if err != nil {
		return raw
	}
	values.Del("prettyPrint")
	values.Del("alt")
	return values.Encode()
}

func googleEnv() config.Env {
	return config.Map(map[string]string{
		"GOOGLE_OAUTH_CLIENT_ID":     "golden-google-client-id",
		"GOOGLE_OAUTH_CLIENT_SECRET": "golden-google-client-secret",
		"GOOGLE_REFRESH_TOKEN":       "golden-google-refresh-token",
		"ATRIUM_TZ":                  "America/Port_of_Spain",
	})
}

func replayGoogle(t *testing.T, scenario string) {
	t.Helper()
	srv := golden.ReplayWith(t, golden.LoadScenario(t, "google_auth", scenario).Exchanges, normalizeQuery)
	t.Cleanup(UseEndpoints(Endpoints{Token: srv.URL + "/token", GoogleAPI: srv.URL + "/calendar/v3/"}))
}

func goldenSpec(t *testing.T, uid string, raw py.Dict) Spec {
	t.Helper()
	tz := zoneOf(t, "America/Port_of_Spain")
	spec, err := NormalizeSpec(raw, tz, uid)
	if err != nil {
		t.Fatal(err)
	}
	return spec
}

func TestGoogleEventCreateGolden(t *testing.T) {
	replayGoogle(t, "event_create")
	ctx := context.Background()
	tz := zoneOf(t, "America/Port_of_Spain")
	timed := goldenSpec(t, "dentist1", py.Dict{{Key: "title", Value: "Dentist"}, {Key: "start", Value: "2026-10-20T13:00"}, {Key: "location", Value: "Main St"}, {Key: "notes", Value: "bring card"}})
	allDay := goldenSpec(t, "holiday1", py.Dict{{Key: "title", Value: "Holiday"}, {Key: "start", Value: "2026-10-30"}, {Key: "all_day", Value: true}})
	for _, spec := range []Spec{timed, allDay} {
		ref, err := CreateEvent(ctx, googleEnv(), Google, spec, tz)
		if err != nil || ref.UID != spec.UID || ref.Calendar != Google {
			t.Fatalf("CreateEvent = %+v, %v", ref, err)
		}
	}
}

func TestGoogleEventDeleteGolden(t *testing.T) {
	replayGoogle(t, "event_delete")
	if err := DeleteEvent(context.Background(), googleEnv(), Google, "dentist1"); err != nil {
		t.Fatal(err)
	}
}

func TestGoogleAlreadyAppliedGolden(t *testing.T) {
	replayGoogle(t, "already_applied")
	ctx := context.Background()
	tz := zoneOf(t, "America/Port_of_Spain")
	spec := goldenSpec(t, "dentist1", py.Dict{{Key: "title", Value: "Dentist"}, {Key: "start", Value: "2026-10-20T13:00"}})
	if _, err := CreateEvent(ctx, googleEnv(), Google, spec, tz); err != nil {
		t.Fatalf("409 insert: %v", err)
	}
	for range 2 {
		if err := DeleteEvent(ctx, googleEnv(), Google, "dentist1"); err != nil {
			t.Fatalf("gone delete: %v", err)
		}
	}
}

func TestGoogleEventsListGolden(t *testing.T) {
	replayGoogle(t, "events_list")
	got, err := ListEvents(context.Background(), googleEnv(), "2026-10-20", "2026-10-27", Google)
	if err != nil {
		t.Fatal(err)
	}
	want := []Row{
		{UID: "e1", Calendar: Google, Start: "2026-10-20T09:00:00-04:00", End: "2026-10-20T09:30:00-04:00", Title: "Standup"},
		{UID: "e3", Calendar: Google, Start: "2026-10-30", End: "2026-10-31", Title: "Holiday", Location: "Home"},
	}
	if len(got.Events) != len(want) || got.Events[0] != want[0] || got.Events[1] != want[1] || got.Truncated || len(got.Notes) != 0 {
		t.Errorf("listing = %+v, want %+v", got, want)
	}
}

func TestListDegradedGolden(t *testing.T) {
	empty := config.Map(map[string]string{"ATRIUM_TZ": "America/Port_of_Spain"})
	_, err := ListEvents(context.Background(), empty, "2026-10-20", "2026-10-27", "")
	var missing NotConfigured
	if !errors.As(err, &missing) {
		t.Fatalf("err = %v", err)
	}
	const want = "ATRIUM_CALENDAR_DIR not set: iCloud mirror skipped; google: GOOGLE_OAUTH_CLIENT_ID, GOOGLE_OAUTH_CLIENT_SECRET, GOOGLE_REFRESH_TOKEN not set"
	if missing.Error() != want {
		t.Errorf("message %q, want %q", missing.Error(), want)
	}
}

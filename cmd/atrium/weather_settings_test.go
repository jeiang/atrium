package main

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/weather"
)

func TestWeatherLocationSettingsSaveAndRefuseOutOfRangeValues(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	setEnv(t, "ATRIUM_DB", filepath.Join(dir, "atrium.db"))
	setEnv(t, "ATRIUM_CACHE_DIR", filepath.Join(dir, "cache"))
	setEnv(t, "ATRIUM_PUBLIC_HOST", "")
	setEnv(t, "ATRIUM_CONFIG", "")

	for key, value := range map[string]string{
		weather.KeyLatitude:     "10.65",
		weather.KeyLongitude:    "-61.5",
		weather.KeyLocationName: "Port of Spain",
	} {
		if got := runSettings(t, "set", key, value); got.code != cli.ExitOK {
			t.Fatalf("set %s: exit %d stderr %q", key, got.code, got.stderr)
		}
		row := runSettings(t, "get", key).row(t)
		if row.Value == nil || *row.Value != value || row.Source == nil || *row.Source != "db" {
			t.Errorf("%s = %+v, want %q from the database", key, row, value)
		}
	}
	if got := runSettings(t, "set", weather.KeyLatitude, "91"); got.code != cli.ExitUsage || !strings.Contains(got.stderr, "weather_latitude must be between -90 and 90") {
		t.Fatalf("out-of-range latitude: exit %d stderr %q", got.code, got.stderr)
	}
	if row := runSettings(t, "get", weather.KeyLatitude).row(t); row.Value == nil || *row.Value != "10.65" {
		t.Errorf("a refused latitude changed the stored value: %+v", row)
	}
}

func TestWeatherConnectionTestCoversForecastAndGeocoding(t *testing.T) {
	t.Parallel()
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	fakeOpenMeteo(t, dir)
	tested := runApp(t, "connections", "test", "service/weather")
	if tested.code != cli.ExitOK || !strings.Contains(tested.stdout, "ok") {
		t.Fatalf("connections test service/weather: exit %d stdout %q stderr %q", tested.code, tested.stdout, tested.stderr)
	}
	if got := connectionState(t, dir, "service/weather"); got != "ok" {
		t.Fatalf("weather connection state = %q", got)
	}
}

func TestWeatherConnectionTestReportsAFailedRequest(t *testing.T) {
	t.Parallel()
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	setEnv(t, "ATRIUM_HOST_CONFIG", weatherHost(t, dir, srv.URL+"/v1/forecast", srv.URL+"/v1/search"))
	tested := runApp(t, "connections", "test", "service/weather")
	if tested.code == cli.ExitOK || !strings.Contains(tested.stdout+tested.stderr, "failing") {
		t.Fatalf("a failing weather endpoint reported success: exit %d stdout %q stderr %q", tested.code, tested.stdout, tested.stderr)
	}
	if got := connectionState(t, dir, "service/weather"); got != "failing" {
		t.Fatalf("weather connection state = %q, want failing", got)
	}
}

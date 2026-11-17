package main

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/apps/weather"
	"github.com/jeiang/atrium/internal/cli"
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

func TestWeatherConnectionIsUnconfiguredUntilALocationIsSetThenCoversForecastAndGeocoding(t *testing.T) {
	t.Parallel()
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	fakeOpenMeteo(t, dir)
	for _, key := range []string{weather.KeyLocationName, weather.KeyLatitude, weather.KeyLongitude} {
		if got := runSettings(t, "unset", key); got.code != cli.ExitOK {
			t.Fatalf("unset %s: exit %d stderr %q", key, got.code, got.stderr)
		}
	}
	unlocated := runApp(t, "connections", "test", "service/weather")
	if unlocated.code == cli.ExitOK || !strings.Contains(unlocated.stdout+unlocated.stderr, "no weather location is set") {
		t.Fatalf("weather test without a location: exit %d stdout %q stderr %q", unlocated.code, unlocated.stdout, unlocated.stderr)
	}
	if got := connectionState(t, dir, "service/weather"); got != "unconfigured" {
		t.Fatalf("weather connection state without a location = %q, want unconfigured", got)
	}
	setTestLocation(t)
	tested := runApp(t, "connections", "test", "service/weather")
	if tested.code != cli.ExitOK || !strings.Contains(tested.stdout, "ok") {
		t.Fatalf("connections test service/weather: exit %d stdout %q stderr %q", tested.code, tested.stdout, tested.stderr)
	}
	if got := connectionState(t, dir, "service/weather"); got != "ok" {
		t.Fatalf("weather connection state = %q", got)
	}
}

func setTestLocation(t *testing.T) {
	t.Helper()
	for key, value := range map[string]string{weather.KeyLatitude: "1.5", weather.KeyLongitude: "2.5"} {
		if got := runSettings(t, "set", key, value); got.code != cli.ExitOK {
			t.Fatalf("set %s: exit %d stderr %q", key, got.code, got.stderr)
		}
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
	setTestLocation(t)
	tested := runApp(t, "connections", "test", "service/weather")
	if tested.code == cli.ExitOK || !strings.Contains(tested.stdout+tested.stderr, "failing") {
		t.Fatalf("a failing weather endpoint reported success: exit %d stdout %q stderr %q", tested.code, tested.stdout, tested.stderr)
	}
	if got := connectionState(t, dir, "service/weather"); got != "failing" {
		t.Fatalf("weather connection state = %q, want failing", got)
	}
}

func TestWeatherLocationSavesThroughTheSettingsPatchOnBothPrefixes(t *testing.T) {
	f := newFixture(t, goldenData{}, nil, "")
	token := localAPIToken(t, "api")
	base, _, stop, err := startServeAppArgs(t, f.app, &lockedBuffer{}, "--auth", "local")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	s := &apiSite{t: t, base: base}
	browser := newWebClient(t, base, nil)
	browser.session("local")
	page := func() (string, map[string]string) {
		t.Helper()
		got := s.do(http.MethodGet, "/api/v1/settings?group=assistant&limit=100", token, nil)
		data := apiData(t, got, http.StatusOK)
		values := map[string]string{}
		for _, raw := range data["items"].([]any) {
			item := raw.(map[string]any)
			if key, _ := item["key"].(string); strings.HasPrefix(key, "weather_") {
				value, _ := item["value"].(string)
				values[key] = value
			}
		}
		rev, _ := got.json["revision"].(string)
		return rev, values
	}
	start, values := page()
	for _, key := range []string{weather.KeyLocationName, weather.KeyLatitude, weather.KeyLongitude} {
		if _, ok := values[key]; !ok {
			t.Fatalf("GET /settings omits %s: %v", key, values)
		}
	}
	set := map[string]any{weather.KeyLocationName: "Port of Spain", weather.KeyLatitude: "10.65", weather.KeyLongitude: "-61.5"}
	apiData(t, browser.do(http.MethodPatch, "/web/v1/settings", map[string]any{"set": set}, "If-Match", `W/"`+start+`"`), http.StatusOK)
	next, values := page()
	if values[weather.KeyLocationName] != "Port of Spain" || values[weather.KeyLatitude] != "10.65" || values[weather.KeyLongitude] != "-61.5" {
		t.Fatalf("weather location after the web patch: %v", values)
	}
	expectAPI(t, browser.do(http.MethodPatch, "/web/v1/settings", map[string]any{"set": map[string]any{weather.KeyLocationName: "Arima"}}, "If-Match", `W/"`+start+`"`), http.StatusPreconditionFailed, "precondition_failed")
	expectAPI(t, s.do(http.MethodPatch, "/api/v1/settings", token, map[string]any{"set": map[string]any{weather.KeyLatitude: "91"}}, "If-Match", `W/"`+next+`"`), http.StatusUnprocessableEntity, "invalid")
	apiData(t, s.do(http.MethodPatch, "/api/v1/settings", token, map[string]any{"set": map[string]any{weather.KeyLocationName: "Arima"}}, "If-Match", `W/"`+next+`"`), http.StatusOK)
	if _, values := page(); values[weather.KeyLocationName] != "Arima" || values[weather.KeyLatitude] != "10.65" {
		t.Fatalf("weather location after the API patch: %v", values)
	}
}

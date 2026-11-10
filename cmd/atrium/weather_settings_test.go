package main

import (
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/weather"
)

func TestWeatherLocationSettingsSaveFromTheIntegrationsTab(t *testing.T) {
	t.Parallel()
	base := startServe(t)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar}
	status, body, _ := fetch(t, client, http.MethodGet, base+"/settings/integrations", nil)
	if status != http.StatusOK {
		t.Fatalf("GET /settings/integrations = %d", status)
	}
	for _, want := range []string{
		`action="/settings/integrations/open_meteo/settings"`,
		`name="weather_latitude"`,
		`name="weather_longitude"`,
		`name="weather_location_name"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("the integrations page lacks %q", want)
		}
	}
	status, body, _ = fetch(t, client, http.MethodPost, base+"/settings/integrations/open_meteo/settings", url.Values{
		"weather_latitude":      {"10.65"},
		"weather_longitude":     {"-61.5"},
		"weather_location_name": {"Port of Spain"},
	})
	if status != http.StatusOK {
		t.Fatalf("POST the weather location = %d: %.300s", status, body)
	}
	for _, want := range []string{`value="10.65"`, `value="-61.5"`, `value="Port of Spain"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the saved page lacks %q", want)
		}
	}
	for key, want := range map[string]string{
		weather.KeyLatitude:     "10.65",
		weather.KeyLongitude:    "-61.5",
		weather.KeyLocationName: "Port of Spain",
	} {
		row := runSettings(t, "get", key).row(t)
		if row.Value == nil || *row.Value != want || row.Source == nil || *row.Source != "db" {
			t.Errorf("%s = %+v, want %q from the database", key, row, want)
		}
	}
	status, body, _ = fetch(t, client, http.MethodPost, base+"/settings/integrations/open_meteo/settings", url.Values{
		"weather_latitude":  {"91"},
		"weather_longitude": {"-61.5"},
	})
	if status != http.StatusUnprocessableEntity || !strings.Contains(body, "weather_latitude must be between -90 and 90") {
		t.Fatalf("out-of-range latitude = %d: %.300s", status, body)
	}
	if row := runSettings(t, "get", weather.KeyLatitude).row(t); row.Value == nil || *row.Value != "10.65" {
		t.Errorf("a refused latitude changed the stored value: %+v", row)
	}
}

func TestPinnedWeatherLocationShowsReadOnly(t *testing.T) {
	t.Parallel()
	setEnv(t, settingEnv(weather.KeyLatitude), "10.65")
	base := startServe(t)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar}
	status, body, _ := fetch(t, client, http.MethodGet, base+"/settings/integrations", nil)
	if status != http.StatusOK {
		t.Fatalf("GET /settings/integrations = %d", status)
	}
	if !strings.Contains(body, `id="weather_latitude" class="set-pinned" data-pinned`) {
		t.Fatalf("the pinned latitude is not read-only: %.500s", body)
	}
	if strings.Contains(body, `name="weather_latitude"`) {
		t.Fatal("the pinned latitude is still editable")
	}
	status, body, _ = fetch(t, client, http.MethodPost, base+"/settings/integrations/open_meteo/settings", url.Values{
		"weather_latitude": {"1"},
	})
	if status != http.StatusUnprocessableEntity || !strings.Contains(body, "weather_latitude is pinned by") {
		t.Fatalf("writing a pinned latitude = %d: %.300s", status, body)
	}
}

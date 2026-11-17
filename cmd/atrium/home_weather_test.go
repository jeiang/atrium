package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/apps/weather"
	weatherapp "github.com/jeiang/atrium/internal/apps/weather/application"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/homecard"
)

type weatherService struct {
	mode    atomic.Value
	release chan struct{}
	once    sync.Once
}

func startWeatherService(t *testing.T, dir string, capabilities string) *weatherService {
	t.Helper()
	ws := &weatherService{release: make(chan struct{})}
	ws.mode.Store("ok")
	reply := openMeteoHandler(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch ws.mode.Load() {
		case "fail":
			w.WriteHeader(http.StatusInternalServerError)
		case "hang":
			select {
			case <-ws.release:
			case <-r.Context().Done():
			}
		default:
			reply(w, r)
		}
	}))
	t.Cleanup(func() {
		ws.once.Do(func() { close(ws.release) })
		srv.Close()
	})
	setEnv(t, "ATRIUM_HOST_CONFIG", hostConfigEdit(t, dir, "host-weather.toml", nil, fmt.Sprintf(`
[connections."service/weather"]
kind = "service/weather"
label = "Weather"
config = { url = %q, geocoding_url = %q }
capabilities = [%s]
`, srv.URL+"/v1/forecast", srv.URL+"/v1/search", capabilities)))
	return ws
}

func homeWeather(t *testing.T, s *apiSite) (homecard.Weather, time.Duration) {
	t.Helper()
	started := time.Now()
	got := s.do(http.MethodGet, "/api/v1/home", s.token("owner"), nil)
	took := time.Since(started)
	var home struct {
		Weather homecard.Weather `json:"weather"`
	}
	if got.status != http.StatusOK {
		t.Fatalf("GET /home: %d %.600s", got.status, got.body)
	}
	if err := json.Unmarshal([]byte(got.body), &home); err != nil {
		t.Fatalf("GET /home: %v %.600s", err, got.body)
	}
	return home.Weather, took
}

func awaitWeather(t *testing.T, s *apiSite, want homecard.State) homecard.Weather {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		w, _ := homeWeather(t, s)
		if w.State == want {
			return w
		}
		if time.Now().After(deadline) {
			t.Fatalf("home weather never reached %q: %+v", want, w)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func assistantRevision(t *testing.T, s *apiSite) int64 {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, s.base+"/api/v1/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+s.token("owner"))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	lines := bufio.NewScanner(resp.Body)
	for lines.Scan() {
		data, ok := strings.CutPrefix(lines.Text(), "data: ")
		if !ok {
			continue
		}
		var sync struct {
			Topics []struct {
				Topic    string `json:"topic"`
				Revision int64  `json:"revision"`
			} `json:"topics"`
		}
		if err := json.Unmarshal([]byte(data), &sync); err != nil {
			t.Fatalf("events sync frame: %v %s", err, data)
		}
		for _, topic := range sync.Topics {
			if topic.Topic == "assistant" {
				return topic.Revision
			}
		}
		t.Fatalf("events sync frame has no assistant topic: %s", data)
	}
	t.Fatalf("events stream ended without a sync frame: %v", lines.Err())
	return 0
}

func setLocation(t *testing.T, kv map[string]string) {
	t.Helper()
	for key, value := range kv {
		if got := runSettings(t, "set", key, value); got.code != cli.ExitOK {
			t.Fatalf("set %s: exit %d stderr %q", key, got.code, got.stderr)
		}
	}
}

func TestHomeWeatherRefreshesInTheBackground(t *testing.T) {
	t.Parallel()
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	startWeatherService(t, dir, `"forecast", "geocoding"`)
	s := startAPISite(t, dir, api)

	if w, _ := homeWeather(t, s); w.State != homecard.StateUnconfigured || w.Current != nil || w.FetchedAt != nil {
		t.Fatalf("weather without a location: %+v", w)
	}

	before := assistantRevision(t, s)
	setLocation(t, map[string]string{weather.KeyLatitude: "1.25", weather.KeyLongitude: "2.5", weather.KeyLocationName: "Lowmarsh"})
	if w, _ := homeWeather(t, s); w.State != homecard.StateLoading || w.Current != nil {
		t.Fatalf("weather before the first reading: %+v", w)
	}
	w := awaitWeather(t, s, homecard.StateOK)
	if w.Location == nil || *w.Location != "Lowmarsh" || w.Current == nil || w.Current.Temperature != 31.5 || w.Today == nil || w.Today.High != 33 || len(w.Hours) != 2 || w.FetchedAt == nil || w.Failure != nil {
		t.Fatalf("weather reading: %+v", w)
	}
	if after := assistantRevision(t, s); after <= before {
		t.Fatalf("assistant topic not published for the reading: %d then %d", before, after)
	}
	setLocation(t, map[string]string{weather.KeyLocationName: "Highfen", weather.KeyLatitude: "3.5"})
	if w, _ := homeWeather(t, s); w.State != homecard.StateLoading || w.Current != nil {
		t.Fatalf("weather after a location change: %+v", w)
	}
	w = awaitWeather(t, s, homecard.StateOK)
	if w.Location == nil || *w.Location != "Highfen" {
		t.Fatalf("weather after a location change: %+v", w)
	}
}

func TestHomeWeatherNeverWaitsForTheService(t *testing.T) {
	t.Parallel()
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	ws := startWeatherService(t, dir, `"forecast", "geocoding"`)
	ws.mode.Store("hang")
	seedSettings(t, map[string]string{weather.KeyLatitude: "1.25", weather.KeyLongitude: "2.5", weather.KeyLocationName: "Lowmarsh"})
	s := startAPISite(t, dir, api)
	for range 3 {
		w, took := homeWeather(t, s)
		if w.State != homecard.StateLoading {
			t.Fatalf("weather while the service hangs: %+v", w)
		}
		if took > time.Second {
			t.Fatalf("GET /home waited %s for a hanging weather service", took)
		}
	}
}

func TestHomeWeatherFailedAndOff(t *testing.T) {
	t.Parallel()
	t.Run("failed", func(t *testing.T) {
		dir, api, _ := moduleFixture(t, flagsExcept("", ""))
		ws := startWeatherService(t, dir, `"forecast", "geocoding"`)
		ws.mode.Store("fail")
		seedSettings(t, map[string]string{weather.KeyLatitude: "1.25", weather.KeyLongitude: "2.5"})
		s := startAPISite(t, dir, api)
		w := awaitWeather(t, s, homecard.StateFailed)
		if w.Failure == nil || *w.Failure != weatherapp.BadReply || w.Current != nil || w.FetchedAt != nil {
			t.Fatalf("failed weather: %+v", w)
		}
	})
	t.Run("off", func(t *testing.T) {
		dir, api, _ := moduleFixture(t, flagsExcept("", ""))
		startWeatherService(t, dir, `"geocoding"`)
		seedSettings(t, map[string]string{weather.KeyLatitude: "1.25", weather.KeyLongitude: "2.5"})
		s := startAPISite(t, dir, api)
		if w, _ := homeWeather(t, s); w.State != homecard.StateOff || w.Current != nil {
			t.Fatalf("weather with the forecast capability off: %+v", w)
		}
	})
}

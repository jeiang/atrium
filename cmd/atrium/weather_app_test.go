package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/apps/weather"
	"github.com/jeiang/atrium/internal/homecard"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/unit"
)

func weatherCard(t *testing.T, s *apiSite, token string) homecard.Weather {
	t.Helper()
	got := s.do(http.MethodGet, "/api/v1/home", token, nil)
	if got.status != http.StatusOK {
		t.Fatalf("GET /home: %d %.600s", got.status, got.body)
	}
	var home struct {
		Weather homecard.Weather `json:"weather"`
	}
	if err := json.Unmarshal([]byte(got.body), &home); err != nil {
		t.Fatalf("GET /home: %v %.600s", err, got.body)
	}
	return home.Weather
}

func weatherToolRun(t *testing.T, api *modelLog) []string {
	t.Helper()
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("done")))
	return offeredTools(t, api, runApp(t, "agent", "run", "--text", "weather", "--kind", "chat"))
}

func TestWeatherAppSwitchesItsToolHomeCardAndConnectionRow(t *testing.T) {
	requireCipher(t)
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	reply := openMeteoHandler(t)
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		reply(w, r)
	}))
	t.Cleanup(srv.Close)
	if _, err := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t)).Write.Exec(
		"UPDATE connections SET config_json = ? WHERE id = 'service/weather' AND config_source = 'operator'",
		`{"url":"`+srv.URL+`/v1/forecast","geocoding_url":"`+srv.URL+`/v1/search"}`); err != nil {
		t.Fatal(err)
	}
	seedSettings(t, map[string]string{weather.KeyLatitude: "10.65", weather.KeyLongitude: "-61.5", weather.KeyLocationName: "Port of Spain"})

	site := &connectionSite{t: t, dir: dir}
	site.start()
	defer func() { site.stop() }()
	token := localAPIToken(t, "weather")
	s := &apiSite{t: t, base: site.base}
	rows, creds := lifecycleCounts(t, dir)

	waitFor(t, func() bool { return weatherCard(t, s, token).State == homecard.StateOK })
	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("w1", "weather", map[string]any{})))
	if names := weatherToolRun(t, api); !slices.Contains(names, "weather") {
		t.Fatalf("weather tool missing while Weather is on: %v", names)
	}
	last := api.Requests()[len(api.Requests())-1]
	if body, _ := json.Marshal(last.JSON(t)["messages"]); !strings.Contains(string(body), "Partly cloudy") {
		t.Fatalf("the weather tool result did not reach the model: %s", body)
	}

	setFlag(t, site, s, unit.Weather, "false")
	waitFor(t, func() bool {
		enabled, state, reason := lifecycleRow(t, dir, "service/weather")
		return !enabled && state == "disabled" && reason == "its app is off"
	})
	before := hits.Load()
	for range 3 {
		if w := weatherCard(t, s, token); w.State != homecard.StateOff || w.Current != nil || w.FetchedAt != nil || w.Hours == nil {
			t.Fatalf("home weather with Weather off: %+v", w)
		}
	}
	if names := weatherToolRun(t, api); slices.Contains(names, "weather") {
		t.Fatalf("weather tool offered while Weather is off: %v", names)
	}
	time.Sleep(500 * time.Millisecond)
	if got := hits.Load(); got != before {
		t.Fatalf("the weather service was called %d times with Weather off", got-before)
	}
	if r, c := lifecycleCounts(t, dir); r != rows || c != creds {
		t.Fatalf("rows %d credentials %d with Weather off, want %d %d", r, c, rows, creds)
	}

	setFlag(t, site, s, unit.Weather, "true")
	waitFor(t, func() bool {
		enabled, state, reason := lifecycleRow(t, dir, "service/weather")
		return enabled && reason == "" && (state == "untested" || state == "ok")
	})
	if names := weatherToolRun(t, api); !slices.Contains(names, "weather") {
		t.Fatalf("weather tool missing after Weather came back: %v", names)
	}
	waitFor(t, func() bool { return weatherCard(t, s, token).State == homecard.StateOK })
	if r, c := lifecycleCounts(t, dir); r != rows || c != creds {
		t.Fatalf("rows %d credentials %d after the switch, want %d %d", r, c, rows, creds)
	}
}

func TestWeatherFlagInheritsTheAssistantOnce(t *testing.T) {
	flagAudit := func(t *testing.T, dir string) (n int, actor, source string) {
		t.Helper()
		d := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
		if err := d.Read.QueryRow("SELECT COUNT(*), COALESCE(MIN(actor), ''), COALESCE(MIN(source), '') FROM settings_audit WHERE key = ?",
			module.FlagKey(unit.Weather)).Scan(&n, &actor, &source); err != nil {
			t.Fatal(err)
		}
		return n, actor, source
	}
	previous := func(assistant string) map[string]string {
		flags := flagsExcept(unit.Assistant, assistant)
		delete(flags, unit.Weather)
		return flags
	}
	restart := func(t *testing.T, dir string) {
		t.Helper()
		_, _, stop := startServeStoppable(t, dir)
		stop()
	}

	t.Run("a database with the assistant on turns Weather on once", func(t *testing.T) {
		dir, _, _ := moduleFixture(t, previous("true"))
		if n, _, _ := flagAudit(t, dir); n != 0 {
			t.Fatalf("the previous-state fixture already has %d weather_enabled audit rows", n)
		}
		restart(t, dir)
		if got := readFlag(t, unit.Weather); got != "true" {
			t.Fatalf("weather_enabled = %s after the upgrade, want true", got)
		}
		if n, actor, source := flagAudit(t, dir); n != 1 || actor != "atrium" || source != "migration" {
			t.Fatalf("weather_enabled audit after the upgrade: %d rows, actor %q, source %q", n, actor, source)
		}
		if got := runSettings(t, "set", module.FlagKey(unit.Weather), "false"); got.code != 0 {
			t.Fatalf("turn Weather off: exit %d %s", got.code, got.stderr)
		}
		restart(t, dir)
		if got := readFlag(t, unit.Weather); got != "false" {
			t.Fatalf("weather_enabled = %s after the operator turned it off and a restart, want false", got)
		}
		if n, _, _ := flagAudit(t, dir); n != 2 {
			t.Fatalf("weather_enabled audit rows = %d, want the seed and the operator's change", n)
		}
	})
	t.Run("a database with the assistant off stays opt-in", func(t *testing.T) {
		dir, _, _ := moduleFixture(t, previous("false"))
		restart(t, dir)
		if got := readFlag(t, unit.Weather); got != "false" {
			t.Fatalf("weather_enabled = %s with the assistant off, want false", got)
		}
		if n, _, _ := flagAudit(t, dir); n != 0 {
			t.Fatalf("weather_enabled audit rows = %d with the assistant off, want 0", n)
		}
	})
	t.Run("a pinned flag is left alone", func(t *testing.T) {
		dir, _, _ := moduleFixture(t, previous("true"))
		setEnv(t, flagEnv(unit.Weather), "false")
		restart(t, dir)
		if n, _, _ := flagAudit(t, dir); n != 0 {
			t.Fatalf("weather_enabled audit rows = %d while pinned, want 0", n)
		}
	})
}

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/weather"
)

func TestWeatherToolReturnsAForecast(t *testing.T) {
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	fakeOpenMeteo(t, dir)

	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("w1", "weather", map[string]any{})))
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("done")))
	if warm := runApp(t, "agent", "run", "--text", "warm"); warm.code != cli.ExitOK {
		t.Fatalf("warm run: exit %d stderr %s", warm.code, warm.stderr)
	}
	ran := runApp(t, "agent", "run", "--text", "weather", "--kind", "chat")
	if ran.code != cli.ExitOK {
		t.Fatalf("weather run: exit %d stderr %s", ran.code, ran.stderr)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), db.Key{})
	defer func() { _ = d.Close() }()
	var content string
	if err := d.Read.QueryRow(
		"SELECT content_json FROM thread_messages WHERE role = 'user' AND content_json LIKE '%Partly cloudy%' " +
			"ORDER BY id DESC LIMIT 1").Scan(&content); err != nil {
		rows, qerr := d.Read.Query("SELECT content_json FROM thread_messages WHERE role = 'user' ORDER BY id DESC LIMIT 5")
		var seen []string
		if qerr == nil {
			for rows.Next() {
				var line string
				if rows.Scan(&line) == nil {
					seen = append(seen, line)
				}
			}
			_ = rows.Close()
		}
		t.Fatalf("no weather tool result: %v; user messages: %s", err, strings.Join(seen, "\n---\n"))
	}
	var blocks []struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal([]byte(content), &blocks); err != nil || len(blocks) == 0 {
		t.Fatalf("weather tool result is not a tool_result block: %v %s", err, content)
	}
	text := blocks[0].Content
	for _, want := range []string{`"temperature_c":31.5`, `"condition":"Partly cloudy"`, `"high_c":33`, `"sunrise":"2026-11-08T06:00"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("weather result misses %q: %s", want, text)
		}
	}
}

func fakeOpenMeteo(t *testing.T, dir string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/forecast":
			if got := r.URL.Query().Get("timezone"); got != "auto" {
				t.Errorf("forecast timezone = %q, want auto", got)
			}
			_, _ = w.Write([]byte(`{"latitude":10.65,"longitude":-61.5,"timezone":"America/Port_of_Spain",` +
				`"current":{"time":"2026-11-08T12:00","temperature_2m":31.5,"apparent_temperature":35.0,` +
				`"relative_humidity_2m":70,"precipitation":0.0,"weather_code":2,"wind_speed_10m":12.0,` +
				`"wind_direction_10m":90,"is_day":1},` +
				`"hourly":{"time":["2026-11-08T12:00","2026-11-08T13:00"],"temperature_2m":[31.5,32.0],` +
				`"precipitation_probability":[10,20],"weather_code":[2,3]},` +
				`"daily":{"time":["2026-11-08","2026-11-09"],"weather_code":[2,61],` +
				`"temperature_2m_max":[33.0,32.0],"temperature_2m_min":[24.0,23.0],` +
				`"precipitation_probability_max":[20,60],` +
				`"sunrise":["2026-11-08T06:00","2026-11-09T06:00"],` +
				`"sunset":["2026-11-08T17:45","2026-11-09T17:45"]}}`))
		case "/v1/search":
			_, _ = w.Write([]byte(`{"results":[{"name":"Port of Spain","country":"Trinidad and Tobago",` +
				`"admin1":"Port of Spain","latitude":10.65,"longitude":-61.5}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	setEnv(t, "ATRIUM_HOST_CONFIG", weatherHost(t, dir, srv.URL+"/v1/forecast", srv.URL+"/v1/search"))
	seedSettings(t, map[string]string{weather.KeyLatitude: "10.65", weather.KeyLongitude: "-61.5", weather.KeyLocationName: "Port of Spain"})
}

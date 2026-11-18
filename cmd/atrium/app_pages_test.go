package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/apps/serverops"
	opsapp "github.com/jeiang/atrium/internal/apps/serverops/application"
	"github.com/jeiang/atrium/internal/apps/weather"
	weatherapp "github.com/jeiang/atrium/internal/apps/weather/application"
	"github.com/jeiang/atrium/internal/homecard"
)

func pageRead[T any](t *testing.T, got apiReply) (T, string) {
	t.Helper()
	var out struct {
		Data T `json:"data"`
	}
	if got.status != http.StatusOK {
		t.Fatalf("status %d: %.600s", got.status, got.body)
	}
	if err := json.Unmarshal([]byte(got.body), &out); err != nil {
		t.Fatalf("%v: %.600s", err, got.body)
	}
	return out.Data, got.header.Get("ETag")
}

func appNav(t *testing.T, s *apiSite, id string) []string {
	t.Helper()
	apps, _ := pageRead[struct {
		Apps []struct {
			ID  string `json:"id"`
			Nav []struct {
				URL string `json:"url"`
			} `json:"nav"`
		} `json:"apps"`
	}](t, s.do(http.MethodGet, "/api/v1/apps", s.token("owner"), nil))
	var urls []string
	for _, app := range apps.Apps {
		if app.ID == id {
			for _, item := range app.Nav {
				urls = append(urls, item.URL)
			}
		}
	}
	return urls
}

func TestWeatherPageShowsTheWeekForTheConfiguredPlace(t *testing.T) {
	t.Parallel()
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	startWeatherService(t, dir, `"forecast", "geocoding"`)
	s := startAPISite(t, dir, api)
	owner := s.token("owner")

	if nav := appNav(t, s, "weather"); !slices.Contains(nav, "/apps/weather") {
		t.Fatalf("weather app nav = %v, want the weather page", nav)
	}
	if page, _ := pageRead[weatherapp.WeatherPage](t, s.do(http.MethodGet, "/api/v1/weather", owner, nil)); page.State != homecard.StateUnconfigured || page.Current != nil || len(page.Days) != 0 {
		t.Fatalf("weather page without a location: %+v", page)
	}
	setLocation(t, map[string]string{weather.KeyLatitude: "1.25", weather.KeyLongitude: "2.5", weather.KeyLocationName: "Lowmarsh"})
	var page weatherapp.WeatherPage
	var etag string
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(200 * time.Millisecond) {
		page, etag = pageRead[weatherapp.WeatherPage](t, s.do(http.MethodGet, "/api/v1/weather", owner, nil))
		if page.State == homecard.StateOK {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("weather page never loaded: %+v", page)
		}
	}
	if page.Location == nil || *page.Location != "Lowmarsh" || page.Timezone == nil || *page.Timezone != "America/Port_of_Spain" ||
		page.Current == nil || page.Current.Temperature != 31.5 || page.Current.WindSpeed != 12 || len(page.Hours) != 2 ||
		len(page.Days) != 2 || page.Days[1].Condition != "Slight rain" || page.Days[1].Precipitation != 60 || page.FetchedAt == nil {
		t.Fatalf("weather page: %+v", page)
	}
	if etag == "" {
		t.Fatal("weather page has no ETag")
	}
	if again := s.do(http.MethodGet, "/api/v1/weather", owner, nil, "If-None-Match", etag); again.status != http.StatusNotModified {
		t.Fatalf("conditional weather read = %d", again.status)
	}
	if home, _ := homeWeather(t, s); home.Today == nil || home.Today.Date != page.Days[0].Date || home.Current == nil || home.Current.Temperature != page.Current.Temperature {
		t.Fatalf("home card %+v does not match the weather page %+v", home, page)
	}
	web, _ := pageRead[weatherapp.WeatherPage](t, webProxyOwnerG2(t, s.base).do(http.MethodGet, "/web/v1/weather", nil))
	if web.State != homecard.StateOK || len(web.Days) != len(page.Days) {
		t.Fatalf("web weather page: %+v", web)
	}
}

func TestServerOpsPageShowsLatestSamplesAndFindings(t *testing.T) {
	t.Parallel()
	fake := &fakeOpsGrafana{up: 1}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch strings.TrimPrefix(r.URL.Path, "/grafana") {
		case "/api/user":
			_, _ = w.Write([]byte(`{"login":"viewer"}`))
		case "/api/search":
			_, _ = w.Write([]byte(`[]`))
		default:
			fake.ServeHTTP(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	setEnv(t, "ATRIUM_HOST_CONFIG", grafanaHost(t, dir, srv.URL+"/grafana/"))
	setEnv(t, settingEnv(serverops.KeyHosts), "node-a,node-b")
	s := startAPISite(t, dir, api)
	owner := s.token("owner")

	if nav := appNav(t, s, "grafana"); !slices.Contains(nav, "/apps/grafana") {
		t.Fatalf("server ops app nav = %v, want the server ops page", nav)
	}
	empty, _ := pageRead[opsapp.ServerOpsPage](t, s.do(http.MethodGet, "/api/v1/ops", owner, nil))
	if len(empty.Hosts) != 0 || len(empty.Findings) != 0 || empty.GrafanaURL == nil || *empty.GrafanaURL != srv.URL+"/grafana" {
		t.Fatalf("server ops page before any sample: %+v", empty)
	}

	d := asstDB(t, dir)
	now := float64(time.Now().Unix())
	sample := func(ts float64, host, metric string, value float64, state, detail any) {
		asstExec(t, d, "INSERT INTO ops_samples (ts, host, metric, value, state, detail) VALUES (?, ?, ?, ?, ?, ?)", ts, host, metric, value, state, detail)
	}
	sample(now-1800, "node-a", serverops.MetricCPU, 12, nil, nil)
	sample(now-1800, "node-a", serverops.MetricUp, 1, "up", nil)
	sample(now-900, "node-a", serverops.MetricUp, 0, "down", nil)
	sample(now-900, "node-a", serverops.MetricCPU, 71.2, nil, nil)
	sample(now-900, "node-a", serverops.MetricAlerts, 2, nil, `{"alerts":["HighCPU","NodeDown"]}`)
	sample(now-600, "node-b", serverops.MetricUp, 1, "up", nil)
	sample(now-600, "node-b", serverops.MetricDisk, 40, nil, nil)
	sample(now-600, "node-b", serverops.MetricAlerts, 0, nil, `{"alerts":[]}`)
	finding := func(fact, title, body string, expires, updated float64) {
		asstExec(t, d, "INSERT INTO kb_records (title, aliases, body, scope, private, marks, writer, origin, pinned, expires_at, created_at, updated_at) "+
			"VALUES (?, ?, ?, 'ops', 0, 0, 'worker', ?, 0, ?, ?, ?)", title, `["`+fact+`"]`, body, "ops:"+fact, expires, updated, updated)
	}
	finding("node-a:up", "Host node-a is down", "critical · node-a:up\n\nup changed from up to down.", now+3600, now-60)
	finding("node-b:disk_root_pct", "Disk filling on node-b", "warning · node-b:disk_root_pct\n\nroot at 40%.", now+3600, now-120)
	finding("node-b:up", "Host node-b recovered", "info · node-b:up\n\nback up.", now-1, now-30)

	before := len(fake.seen())
	page, etag := pageRead[opsapp.ServerOpsPage](t, s.do(http.MethodGet, "/api/v1/ops", owner, nil))
	if got := len(fake.seen()); got != before {
		t.Errorf("the server ops page sent %d Grafana requests, want none", got-before)
	}
	if len(page.Hosts) != 2 {
		t.Fatalf("server ops hosts: %+v", page.Hosts)
	}
	a, b := page.Hosts[0], page.Hosts[1]
	if a.Host != "node-a" || a.SampledAt != now-900 || a.Up == nil || *a.Up || a.CPU == nil || *a.CPU != 71.2 || a.Disk != nil ||
		!slices.Equal(a.Alerts, []string{"HighCPU", "NodeDown"}) {
		t.Errorf("node-a = %+v, want its latest pass only", a)
	}
	if b.Host != "node-b" || b.Up == nil || !*b.Up || b.Disk == nil || *b.Disk != 40 || b.CPU != nil || len(b.Alerts) != 0 {
		t.Errorf("node-b = %+v", b)
	}
	if len(page.Findings) != 2 || page.Findings[0].Fact != "node-a:up" || page.Findings[0].Severity != "critical" ||
		page.Findings[0].Body != "up changed from up to down." || page.Findings[1].Severity != "warning" || page.Findings[1].Title != "Disk filling on node-b" {
		t.Errorf("server ops findings = %+v, want the two live findings newest first", page.Findings)
	}
	if etag == "" {
		t.Fatal("server ops page has no ETag")
	}
	if again := s.do(http.MethodGet, "/api/v1/ops", owner, nil, "If-None-Match", etag); again.status != http.StatusNotModified {
		t.Fatalf("conditional server ops read = %d", again.status)
	}
	sample(now, "node-b", serverops.MetricUp, 0, "down", nil)
	if changed := s.do(http.MethodGet, "/api/v1/ops", owner, nil, "If-None-Match", etag); changed.status != http.StatusOK || !strings.Contains(changed.body, `"sampled_at":`) {
		t.Fatalf("server ops read after a new sample = %d %.300s", changed.status, changed.body)
	}
	web, _ := pageRead[opsapp.ServerOpsPage](t, webProxyOwnerG2(t, s.base).do(http.MethodGet, "/web/v1/ops", nil))
	if len(web.Hosts) != 2 || web.Hosts[1].Up == nil || *web.Hosts[1].Up || len(web.Hosts[1].Alerts) != 0 {
		t.Fatalf("web server ops page: %+v", web)
	}
}

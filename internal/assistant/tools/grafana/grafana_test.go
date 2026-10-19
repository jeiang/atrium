package grafanatools

import (
	"context"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/assistant/registry/registrytest"
	"github.com/jeiang/atrium/internal/assistant/tool"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/testutil/golden"
)

const series = `{"series": [{"fields": ["Time", "Value"], "rows": [["2023-11-14T22:13:20+00:00", 1], ["2023-11-14T22:14:20+00:00", 0.5]], "total_rows": 2, "name": "up", "labels": {"instance": "a"}}], "total_series": 1}`

func run(t *testing.T, scenario string, calls func(call func(name, input string) (any, error))) {
	t.Helper()
	srv := golden.Replay(t, golden.LoadScenario(t, "grafana", scenario).Exchanges)
	tc := &tool.Context{Env: config.Map(map[string]string{"GRAFANA_URL": srv.URL + "/", "GRAFANA_VIEWER_TOKEN": " golden-grafana-token "})}
	byName := map[string]tool.Tool{}
	for _, tl := range Tools() {
		byName[tl.Name] = tl
	}
	calls(func(name, input string) (any, error) {
		args, err := py.JSONLoads([]byte(input))
		if err != nil {
			t.Fatal(err)
		}
		return byName[name].Handler(context.Background(), tc, args.(py.Dict))
	})
}

func expect(t *testing.T, got any, err error, want string) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	golden.EqualJSON(t, []byte(want), []byte(py.JSONDumps(got)))
}

func TestToolsMatchPython(t *testing.T) {
	for _, tl := range Tools() {
		registrytest.Verify(t, tl)
	}
}

func TestGoldenTools(t *testing.T) {
	run(t, "tools", func(call func(string, string) (any, error)) {
		got, err := call("grafana_datasources", `{}`)
		expect(t, got, err, `{"datasources": [{"uid": "p1", "name": "Prom", "type": "prometheus"}, {"uid": "l1", "name": "Loki", "type": "loki"}]}`)
		got, err = call("grafana_query", `{"datasource_uid": "p1", "expr": "up"}`)
		expect(t, got, err, series)
		got, err = call("grafana_query", `{"datasource_uid": "p1", "expr": "rate(http_requests_total{job=\"api\"}[5m])", "step": "60s", "start": "now-6h", "end": "now-1h"}`)
		expect(t, got, err, series)
		got, err = call("grafana_query", `{"datasource_uid": "l1", "expr": "{unit=\"atrium-web.service\"} |= \"error\"", "kind": "logs", "limit": 150}`)
		expect(t, got, err, series)
		got, err = call("grafana_alerts", `{}`)
		expect(t, got, err, `{"total_firing": 1, "alerts": [{"labels": {"alertname": "Down"}, "summary": "host down", "active_at": "2026-10-15T12:00:00Z", "value": "0"}]}`)
		dashboards := `{"dashboards": [{"title": "Node", "uid": "n1", "url": "/d/n1/node", "folder": "Infra", "tags": ["linux"]}]}`
		got, err = call("grafana_search_dashboards", `{"query": "node"}`)
		expect(t, got, err, dashboards)
		got, err = call("grafana_search_dashboards", `{}`)
		expect(t, got, err, dashboards)
	})
}

func TestGoldenQueryError(t *testing.T) {
	run(t, "query_error", func(call func(string, string) (any, error)) {
		_, err := call("grafana_query", `{"datasource_uid": "p1", "expr": "up{"}`)
		msg, ok := tool.Message(err)
		if !ok || !strings.HasPrefix(msg, "grafana 400: ") || !strings.Contains(msg, "parse error at char 3") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestNotConfigured(t *testing.T) {
	tc := &tool.Context{Env: config.Map(map[string]string{"GRAFANA_URL": "http://x"})}
	_, err := Tools()[0].Handler(context.Background(), tc, py.Dict{})
	if msg, ok := tool.Message(err); !ok || msg != "GRAFANA_URL and GRAFANA_VIEWER_TOKEN must be set" {
		t.Fatalf("err = %v", err)
	}
}

func TestMalformedURLIsUnreachable(t *testing.T) {
	for url, class := range map[string]string{
		"grafana.local:3000":  "UnsupportedProtocol",
		"not a url":           "UnsupportedProtocol",
		"ftp://x":             "UnsupportedProtocol",
		"/relative":           "UnsupportedProtocol",
		"http://":             "ConnectError",
		"http:///path":        "ConnectError",
		"http://[bad":         "ConnectError",
		"http://exa mple.com": "ConnectError",
		"http://host:99999":   "ConnectError",
	} {
		tc := &tool.Context{Env: config.Map(map[string]string{"GRAFANA_URL": url, "GRAFANA_VIEWER_TOKEN": "t"})}
		for _, tl := range Tools() {
			if tl.Name != "grafana_datasources" {
				continue
			}
			_, err := tl.Handler(context.Background(), tc, py.Dict{})
			if msg, ok := tool.Message(err); !ok || msg != "grafana unreachable: "+class {
				t.Errorf("%q: err = %v, want %s", url, err, class)
			}
		}
	}
}

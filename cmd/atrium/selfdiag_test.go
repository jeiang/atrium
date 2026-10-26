package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/jeiang/atrium/internal/anthropic"
	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	grafanatools "github.com/jeiang/atrium/internal/assistant/tools/grafana"
	"github.com/jeiang/atrium/internal/cli"
)

type dsQuery struct {
	RefID      string         `json:"refId"`
	Expr       string         `json:"expr"`
	Datasource map[string]any `json:"datasource"`
}

func fakeGrafana(t *testing.T) (*httptest.Server, func() []dsQuery) {
	t.Helper()
	var mu sync.Mutex
	var seen []dsQuery
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/ds/query" {
			http.NotFound(w, r)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		var body struct{ Queries []dsQuery }
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("grafana body %s: %v", raw, err)
		}
		mu.Lock()
		seen = append(seen, body.Queries...)
		mu.Unlock()
		results := map[string]any{}
		for _, q := range body.Queries {
			results[q.RefID] = map[string]any{"frames": []any{map[string]any{
				"schema": map[string]any{"name": q.RefID, "fields": []any{
					map[string]any{"name": "Time", "type": "time"},
					map[string]any{"name": "Value", "type": "string", "labels": map[string]any{"job": "backup"}},
				}},
				"data": map[string]any{"values": []any{[]any{1761400000000}, []any{"row-for-" + q.RefID}}},
			}}}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"results": results})
	}))
	t.Cleanup(srv.Close)
	return srv, func() []dsQuery {
		mu.Lock()
		defer mu.Unlock()
		return append([]dsQuery(nil), seen...)
	}
}

func TestSelfDiagnoseQueriesMetricsAndLogsInTheConfiguredDialect(t *testing.T) {
	for _, tc := range []struct {
		dialect, want string
	}{
		{grafanatools.Loki, `{unit=~"atrium.*"} |~ "(?i)(error|warn)"`},
		{grafanatools.VictoriaLogs, `_stream:{unit=~"atrium.*"} (error OR warn)`},
	} {
		t.Run(tc.dialect, func(t *testing.T) {
			moduleFixture(t, flagsExcept("", ""))
			grafana, seen := fakeGrafana(t)
			t.Setenv(settingEnv(grafanatools.KeyURL), grafana.URL)
			t.Setenv(settingEnv(grafanatools.KeyMetricsUID), "vm-uid")
			t.Setenv(settingEnv(grafanatools.KeyLogsUID), "logs-uid")
			t.Setenv(settingEnv(grafanatools.KeyLogsType), tc.dialect)
			api := &modelLog{Server: anthropictest.New(t,
				anthropictest.Reply("tool_use", anthropictest.ToolUse("c1", "atrium_self_diagnose", map[string]any{"scope": "both", "minutes": 30})),
				anthropictest.Reply("end_turn", anthropictest.Text("healthy")),
			)}
			api.OnRequest = func(r anthropictest.Request) {
				api.mu.Lock()
				defer api.mu.Unlock()
				api.seen = append(api.seen, r)
			}
			t.Setenv(anthropic.BaseURLEnv, api.URL)

			ran := runApp("agent", "run", "--text", "how are you doing?", "--kind", "chat")
			if ran.code != cli.ExitOK {
				t.Fatalf("agent run: %d %s", ran.code, ran.stderr)
			}
			var metrics, logs []dsQuery
			for _, q := range seen() {
				switch q.Datasource["uid"] {
				case "logs-uid":
					logs = append(logs, q)
				case "vm-uid":
					metrics = append(metrics, q)
				}
			}
			if len(logs) != 1 || logs[0].Expr != tc.want {
				t.Errorf("log queries %+v, want one %q", logs, tc.want)
			}
			if len(metrics) == 0 || metrics[0].Expr != "atrium_job_stale == 1" {
				t.Errorf("metric queries %+v, want the stale-job query first", metrics)
			}
			requests := api.Requests()
			if len(requests) != 2 {
				t.Fatalf("%d model requests, want 2", len(requests))
			}
			result := string(requests[1].Body)
			for _, want := range []string{"row-for-stale_jobs", "row-for-logs", "untrusted"} {
				if !strings.Contains(result, want) {
					t.Errorf("tool result lacks %q", want)
				}
			}
		})
	}
}

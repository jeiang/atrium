package main

import (
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	grafanatools "github.com/jeiang/atrium/internal/assistant/tools/grafana"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/endpoint"
	"github.com/jeiang/atrium/internal/serverops"
)

func condenserFixture(t *testing.T, fake *fakeOpsGrafana, chat *fakeOpenAI) (string, *modelLog, *fixture, *db.DB) {
	t.Helper()
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	dir, api, f := moduleFixture(t, flagsExcept("", ""))
	setEnv(t, settingEnv(grafanatools.KeyURL), srv.URL)
	setEnv(t, hostEnv("grafana", grafanatools.KeyMetricsUID), "metrics")
	setEnv(t, settingEnv(serverops.KeyHosts), "node-a")
	setEnv(t, settingEnv(endpoint.KeyChatURL), chat.URL+"/v1")
	setEnv(t, settingEnv(endpoint.KeyChatModel), "qwen")
	setEnv(t, settingEnv(serverops.KeyCondenser), serverops.CondenserLocal)
	warm(t, api)
	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
	t.Cleanup(func() { _ = d.Close() })
	runOpsSampler(t, f, d, samplesAtLeast(t, d, 6))
	return dir, api, f, d
}

func TestLocalCondenserFilesFindingsWithoutAnthropic(t *testing.T) {
	fake := &fakeOpsGrafana{up: 1}
	chat := newFakeOpenAI(t)
	_, api, f, d := condenserFixture(t, fake, chat)
	before := len(api.Requests())
	fake.setUp(0)
	fake.setAlerts([]map[string]any{{"state": "firing", "labels": map[string]any{"alertname": "HighCPU", "instance": "node-a:9100"}}})
	chat.setChatReply(`{"findings":[{"fact":"node-a:up","severity":"warning","title":"Host node-a is down","body":"up changed from up to down."}]}`)
	finished := samplerFinished(t, d)
	runOpsSampler(t, f, d, func() bool {
		_, _, found := workerFinding(t, d, "node-a:up")
		return found && finished()
	})
	if len(api.Requests()) != before {
		t.Fatalf("local condenser made %d Anthropic requests", len(api.Requests())-before)
	}
	calls, bodies := chat.chatCalls()
	if calls != 1 || len(bodies) != 1 {
		t.Fatalf("chat calls = %d, bodies = %d, want one", calls, len(bodies))
	}
	format, _ := bodies[0]["response_format"].(map[string]any)
	if format["type"] != "json_schema" {
		t.Errorf("response_format = %v", format)
	}
	messages, _ := bodies[0]["messages"].([]any)
	if len(messages) != 2 {
		t.Fatalf("messages = %d, want system and user", len(messages))
	}
	user := fmt.Sprint(messages[1].(map[string]any)["content"])
	if !strings.Contains(user, "OPS_DELTA ") || !strings.Contains(user, "node-a:up") || !strings.Contains(user, "HighCPU") {
		t.Errorf("condenser input = %q", user)
	}
	var severity, title, body string
	var marks int64
	if err := d.Read.QueryRow("SELECT severity, title, body, marks FROM ops_findings WHERE fact = 'node-a:up'").
		Scan(&severity, &title, &body, &marks); err != nil || severity != "warning" || title != "Host node-a is down" || marks != 1 {
		t.Errorf("finding = %q %q %q %d, %v", severity, title, body, marks, err)
	}
	runOpsSampler(t, f, d, samplerFinished(t, d))
	if calls, _ := chat.chatCalls(); calls != 1 {
		t.Errorf("an identical pass made %d condenser calls", calls-1)
	}
	chat.setChatReply(`{"findings":[]}`)
	fake.setUp(1)
	fake.setAlerts(nil)
	runOpsSampler(t, f, d, samplerFinished(t, d))
	waitForOps(t, d, "the finding resolved", func() bool {
		_, resolved, found := workerFinding(t, d, "node-a:up")
		return found && resolved
	})
	if len(api.Requests()) != before {
		t.Errorf("local mode made %d Anthropic requests", len(api.Requests())-before)
	}
}

func TestLocalCondenserInvalidJSONRetries(t *testing.T) {
	fake := &fakeOpsGrafana{up: 1}
	chat := newFakeOpenAI(t)
	_, api, f, d := condenserFixture(t, fake, chat)
	before := len(api.Requests())
	chat.setChatReply(`not json`)
	fake.setUp(0)
	finished := samplerFinished(t, d)
	runOpsSampler(t, f, d, finished)
	var status, jobErr string
	if err := d.Read.QueryRow("SELECT status, COALESCE(error, '') FROM job_runs WHERE job_id = (SELECT id FROM jobs WHERE name = 'ops-sampler') ORDER BY id DESC LIMIT 1").
		Scan(&status, &jobErr); err != nil || status != "error" || !strings.Contains(jobErr, "JSON") {
		t.Fatalf("job run = %q %q, %v", status, jobErr, err)
	}
	if _, _, found := workerFinding(t, d, "node-a:up"); found {
		t.Errorf("an invalid reply wrote a finding")
	}
	if calls, _ := chat.chatCalls(); calls != 1 {
		t.Fatalf("condenser calls = %d, want one", calls)
	}
	chat.setChatReply(`{"findings":[{"fact":"node-a:up","severity":"warning","title":"Host node-a is down","body":"up changed."}]}`)
	finished = samplerFinished(t, d)
	runOpsSampler(t, f, d, func() bool {
		_, _, found := workerFinding(t, d, "node-a:up")
		return found && finished()
	})
	if calls, _ := chat.chatCalls(); calls != 2 {
		t.Errorf("condenser calls after the retry = %d, want two", calls)
	}
	if len(api.Requests()) != before {
		t.Errorf("local mode made %d Anthropic requests", len(api.Requests())-before)
	}
}

func TestLocalCondenserDropsFactsOutsideTheDelta(t *testing.T) {
	fake := &fakeOpsGrafana{up: 1}
	chat := newFakeOpenAI(t)
	_, _, f, d := condenserFixture(t, fake, chat)
	chat.setChatReply(`{"findings":[` +
		`{"fact":"node-a:up","severity":"warning","title":"Host node-a is down","body":"up changed."},` +
		`{"fact":"node-a:cpu_pct","severity":"warning","title":"CPU high","body":"not in the delta."}]}`)
	fake.setUp(0)
	finished := samplerFinished(t, d)
	runOpsSampler(t, f, d, func() bool {
		_, _, found := workerFinding(t, d, "node-a:up")
		return found && finished()
	})
	var up, cpu int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM ops_findings WHERE fact = 'node-a:up'").Scan(&up); err != nil {
		t.Fatal(err)
	}
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM ops_findings WHERE fact = 'node-a:cpu_pct'").Scan(&cpu); err != nil {
		t.Fatal(err)
	}
	if up != 1 || cpu != 0 {
		t.Errorf("findings: up = %d, cpu = %d; want one valid and none outside the delta", up, cpu)
	}
}

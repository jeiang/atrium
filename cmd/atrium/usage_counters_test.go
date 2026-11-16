package main

import (
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/backup"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/passrun"
	"github.com/jeiang/atrium/internal/webusage"
)

func hit(t *testing.T, base, path string, headers ...string) (int, string) {
	t.Helper()
	return hitWith(t, http.DefaultClient, base, path, headers...)
}

func hitWith(t *testing.T, client *http.Client, base, path string, headers ...string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, base+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(body)
}

func usageRoutes(t *testing.T) map[string]webusage.Route {
	t.Helper()
	got := runApp(t, "usage", "routes", "--json")
	if got.code != cli.ExitOK {
		t.Fatalf("usage routes: exit %d stderr %q", got.code, got.stderr)
	}
	var routes []webusage.Route
	if err := json.Unmarshal([]byte(got.stdout), &routes); err != nil {
		t.Fatalf("usage routes JSON: %v\n%s", err, got.stdout)
	}
	out := map[string]webusage.Route{}
	for _, r := range routes {
		out[r.Surface+" "+r.Method+" "+r.Route] = r
	}
	return out
}

func TestRouteUsageCountersSurviveARestartAndSplitAgentTraffic(t *testing.T) {
	moduleFixture(t, flagsExcept("", ""))
	issue := func(args ...string) string {
		got := runApp(t, append([]string{"auth", "token", "issue"}, args...)...)
		if got.code != cli.ExitOK {
			t.Fatalf("issue %v: exit %d stderr %q", args, got.code, got.stderr)
		}
		return strings.TrimSpace(got.stdout)
	}
	plain := issue("--name", "phone")
	agent := issue("--name", "browser-agent", "--agent")
	listed := runApp(t, "auth", "token", "list").stdout
	if row := tokenRow(t, listed, "browser-agent"); row[7] != "agent" {
		t.Fatalf("the agent token is not marked in the list: %v", row)
	}
	if row := tokenRow(t, listed, "phone"); row[7] != "-" {
		t.Fatalf("the plain token is marked: %v", row)
	}
	opsBase := opsAddr(t)
	serve := func() (string, func()) {
		base, _, stop, err := startServeAppArgs(t, fixtureFor(t).app, &lockedBuffer{}, "--auth", "local")
		if err != nil {
			t.Fatal(err)
		}
		return base, stop
	}
	want := func(status, code int, what string) {
		t.Helper()
		if status != code {
			t.Fatalf("%s = %d, want %d", what, status, code)
		}
	}

	base, stop := serve()
	for range 2 {
		code, _ := hit(t, base, "/about")
		want(code, http.StatusOK, "operator page")
	}
	code, _ := hit(t, base, "/about", "Atrium-Agent", "1")
	want(code, http.StatusOK, "agent page")
	code, _ = hit(t, base, "/web/v1/session", "Atrium-Client", "web")
	want(code, http.StatusOK, "web session")
	code, _ = hit(t, base, "/api/v1/me", "Authorization", "Bearer "+plain)
	want(code, http.StatusOK, "plain token")
	for range 2 {
		code, body := hit(t, base, "/api/v1/me", "Authorization", "Bearer "+agent)
		want(code, http.StatusOK, "agent token")
		if !strings.Contains(body, `"agent":true`) {
			t.Fatalf("the agent token identity does not show the mark: %s", body)
		}
	}
	code, _ = hit(t, base, "/no-such-page-7f3a")
	want(code, http.StatusNotFound, "unmatched path")
	stop()

	routes := usageRoutes(t)
	check := func(key string, operator, agentCount int64) {
		t.Helper()
		r, ok := routes[key]
		if !ok || r.Operator != operator || r.Agent != agentCount {
			t.Fatalf("%s = %+v (present %v), want operator %d agent %d; all: %+v", key, r, ok, operator, agentCount, routes)
		}
	}
	check("page GET /about", 2, 1)
	check("web GET /web/v1/session", 1, 0)
	check("api GET /api/v1/me", 1, 2)
	check("page GET unmatched", 1, 0)
	for key := range routes {
		if strings.Contains(key, "7f3a") {
			t.Fatalf("a raw path is stored as a route: %q", key)
		}
	}

	base, stop = serve()
	code, _ = hit(t, base, "/about")
	want(code, http.StatusOK, "operator page after restart")
	body := scrape(t, opsBase)
	if got := scrapeValue(t, body, `atrium_http_route_requests_total{surface="page",method="GET",route="/about",agent="false"}`); got != 2 {
		t.Fatalf("the route metric after a restart = %v, want the 2 saved operator requests:\n%s", got, body)
	}
	if got := scrapeValue(t, body, `atrium_http_route_requests_total{surface="api",method="GET",route="/api/v1/me",agent="true"}`); got != 2 {
		t.Fatalf("the agent API metric after a restart = %v, want 2", got)
	}
	if strings.Contains(body, "7f3a") {
		t.Fatal("a raw path appears as a metric label")
	}
	stop()
	routes = usageRoutes(t)
	check("page GET /about", 3, 1)
}

func TestAgentMarkStaysWithTheSignInSession(t *testing.T) {
	s := newAPISite(t)
	browser := oidcBrowser(t, s.base, s.idp, "owner")
	session := func(headers ...string) string {
		t.Helper()
		code, body := hitWith(t, browser, s.base, "/web/v1/session", append([]string{"Atrium-Client", "web"}, headers...)...)
		if code != http.StatusOK {
			t.Fatalf("web session = %d %s", code, body)
		}
		return body
	}
	if body := session(); !strings.Contains(body, `"agent":false`) {
		t.Fatalf("a fresh sign-in session is marked: %s", body)
	}
	if body := session("Atrium-Agent", "1"); !strings.Contains(body, `"agent":true`) {
		t.Fatalf("the agent header does not mark the session: %s", body)
	}
	if body := session(); !strings.Contains(body, `"agent":true`) {
		t.Fatalf("the mark does not stay with the session: %s", body)
	}
	code, _ := hitWith(t, browser, s.base, "/settings/auth")
	if code != http.StatusOK {
		t.Fatalf("a marked session lost access to a page: %d", code)
	}
	d := mustConnect(t, filepath.Join(s.dir, "atrium.db"), db.Key{})
	defer func() { _ = d.Close() }()
	var marked int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM auth_sessions WHERE agent = 1").Scan(&marked); err != nil || marked != 1 {
		t.Fatalf("marked sessions = %d, %v; want 1", marked, err)
	}
}

func scrapeValue(t *testing.T, body, series string) float64 {
	t.Helper()
	for line := range strings.SplitSeq(body, "\n") {
		if value, ok := strings.CutPrefix(line, series+" "); ok {
			n, err := strconv.ParseFloat(value, 64)
			if err != nil {
				t.Fatalf("%s value %q: %v", series, value, err)
			}
			return n
		}
	}
	return -1
}

func scrape(t *testing.T, opsBase string) string {
	t.Helper()
	status, body, _ := fetch(t, http.DefaultClient, http.MethodGet, opsBase+"/metrics", nil)
	if status != http.StatusOK {
		t.Fatalf("ops /metrics = %d", status)
	}
	return body
}

func TestSchedulerJobRunCounterSurvivesARestart(t *testing.T) {
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	dest := t.TempDir()
	setEnv(t, "ATRIUM_HOST_CONFIG", backupHost(t, dir, "backup_directory = \""+dest+"\""))
	d := asstDB(t, dir)
	declareJobsAndParkThem(t, d)
	now := float64(time.Now().Unix())
	asstExec(t, d, "INSERT INTO jobs (id, name, prompt, schedule, tz, kind, state, created_by, created_at, module, activated_at) VALUES (9001, 'Water the plants', 'remind me', '0 9 * * *', 'UTC', 'cron', 'disabled', 'operator', ?, 'assistant', ?)", now, now)
	asstExec(t, d, "INSERT INTO job_runs (job_id, due_at, started_at, ended_at, status) VALUES (9001, ?, ?, ?, 'ok')", now-60, now-60, now-30)
	asstExec(t, d, "UPDATE jobs SET next_due = ? WHERE declaration_key = ?", now-60, backup.JobKey)
	opsBase := opsAddr(t)
	series := `atrium_scheduler_job_runs_total{scheduled_job="` + passrun.Backup + `",status="ok"}`

	_, _, stop := startServeStoppable(t, dir)
	waitUntil(t, 60*time.Second, "the first backup run", schedulerTerminal(d, passrun.Backup))
	body := scrape(t, opsBase)
	before := scrapeValue(t, body, series)
	if before < 1 {
		t.Fatalf("%s = %v after one backup run, want at least 1:\n%s", series, before, body)
	}
	if got := scrapeValue(t, body, `atrium_scheduler_job_runs_total{scheduled_job="operator_schedule",status="ok"}`); got != 1 {
		t.Errorf("operator schedule runs = %v, want 1 under the shared label", got)
	}
	if strings.Contains(body, "Water the plants") {
		t.Error("an operator schedule name appears as a label value")
	}
	stop()

	asstExec(t, d, "UPDATE jobs SET next_due = ? WHERE declaration_key = ?", float64(time.Now().Unix())-1, backup.JobKey)
	startServeIn(t, dir)
	waitUntil(t, 60*time.Second, "the second backup run", func() bool {
		return schedulerRuns(t, d, passrun.Backup) == 2 && schedulerTerminal(d, passrun.Backup)()
	})
	if after := scrapeValue(t, scrape(t, opsBase), series); after != before+1 {
		t.Fatalf("%s = %v after a restart and one more run, want %v", series, after, before+1)
	}
}

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/apps/library"
	"github.com/jeiang/atrium/internal/apps/mail/tracking"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/ops"
)

type capabilityNeed struct {
	Module       string   `json:"module"`
	Active       bool     `json:"active"`
	Kind         string   `json:"kind"`
	Capability   string   `json:"capability"`
	Soft         bool     `json:"soft"`
	Features     []string `json:"features"`
	Candidates   []string `json:"candidates"`
	Connection   string   `json:"connection"`
	Label        string   `json:"connection_label"`
	State        string   `json:"state"`
	Met          bool     `json:"met"`
	Availability string   `json:"availability"`
	Repair       string   `json:"repair"`
}

type capabilityRepair struct {
	ID         string   `json:"id"`
	Connection string   `json:"connection"`
	Class      string   `json:"class"`
	State      string   `json:"state"`
	Owner      string   `json:"owner"`
	Severity   string   `json:"severity"`
	Features   []string `json:"features"`
	Fix        string   `json:"fix"`
}

type capabilityModel struct {
	Needs   []capabilityNeed   `json:"needs"`
	Repairs []capabilityRepair `json:"repairs"`
}

func capabilityAPI(t *testing.T, base, token, path string) string {
	t.Helper()
	got := (&apiSite{t: t, base: base}).do(http.MethodGet, "/api/v1"+path, token, nil)
	if got.status != http.StatusOK {
		t.Fatalf("GET /api/v1%s: %d %.600s", path, got.status, got.body)
	}
	return got.body
}

func capabilityDependencies(t *testing.T, base, token string) capabilityModel {
	t.Helper()
	body := capabilityAPI(t, base, token, "/connections/dependencies")
	var reply struct{ Data capabilityModel }
	if err := json.Unmarshal([]byte(body), &reply); err != nil {
		t.Fatalf("dependencies body %.600s: %v", body, err)
	}
	return reply.Data
}

func (m capabilityModel) need(t *testing.T, module, capability string) capabilityNeed {
	t.Helper()
	for _, n := range m.Needs {
		if n.Module == module && n.Capability == capability {
			return n
		}
	}
	t.Fatalf("no %s need for %s in %+v", module, capability, m.Needs)
	return capabilityNeed{}
}

func (m capabilityModel) repair(connection string) (capabilityRepair, bool) {
	for _, r := range m.Repairs {
		if r.Connection == connection {
			return r, true
		}
	}
	return capabilityRepair{}, false
}

func noticeBodies(t *testing.T, dir, label string) []string {
	t.Helper()
	rows, err := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t)).Read.Query(
		"SELECT body FROM notifications WHERE title LIKE ? ORDER BY id", "atrium: "+label+" %")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var body string
		if err := rows.Scan(&body); err != nil {
			t.Fatal(err)
		}
		out = append(out, body)
	}
	return out
}

func grafanaPairHost(t *testing.T, dir, first, second string) string {
	t.Helper()
	key := filepath.Join(dir, "grafana-pair.key")
	if err := os.WriteFile(key, []byte("glsa_pair\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	table := ""
	for _, c := range []struct{ id, label, url string }{{"service/grafana", "Grafana A", first}, {"service/grafana-b", "Grafana B", second}} {
		table += fmt.Sprintf(`
[connections.%q]
kind = "service/grafana"
label = %q
config = { url = %q }
capabilities = ["viewer"]
credential_files = { viewer_token = %q }
`, c.id, c.label, c.url, key)
	}
	path := filepath.Join(dir, "host-grafana-pair.toml")
	if err := os.WriteFile(path, []byte(table), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestTwoGrafanaConnectionsBindTheFirstHealthyAndRepairsFollowTheirHealth(t *testing.T) {
	requireCipher(t)
	first, firstStatus := grafanaHealthServer(t)
	second, secondStatus := grafanaHealthServer(t)
	firstStatus.Store(http.StatusInternalServerError)
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	setEnv(t, "ATRIUM_DB_KEY", rightKeyHex)
	setEnv(t, "ATRIUM_HOST_CONFIG", grafanaPairHost(t, dir, first.URL, second.URL))
	token := localAPIToken(t, "capabilities")
	s := &connectionSite{t: t, dir: dir}
	s.start()
	defer func() { s.stop() }()
	waitFor(t, func() bool {
		return connectionState(t, dir, "service/grafana") == "failing" && connectionState(t, dir, "service/grafana-b") == "ok"
	})

	model := capabilityDependencies(t, s.base, token)
	viewer := model.need(t, "grafana", "viewer")
	if viewer.Connection != "service/grafana-b" || viewer.Label != "Grafana B" || !viewer.Met || viewer.Availability != "available" || viewer.Soft ||
		!slices.Equal(viewer.Candidates, []string{"service/grafana", "service/grafana-b"}) {
		t.Fatalf("grafana viewer with the first connection failing = %+v, want bound to the healthy second", viewer)
	}
	repair, ok := model.repair("service/grafana")
	if !ok || repair.ID != "connection:service/grafana:unreachable" || repair.Class != "unreachable" || repair.Severity != "notice" ||
		len(repair.Features) != 0 || repair.Owner != "host" || repair.Fix != "/settings/connections" {
		t.Fatalf("repair for the failing first connection = %+v (found %v), want an unreachable notice with no features", repair, ok)
	}
	if _, ok := model.repair("service/grafana-b"); ok {
		t.Fatalf("a repair is open for the healthy second connection: %+v", model.Repairs)
	}
	if bodies := noticeBodies(t, dir, "Grafana A"); len(bodies) != 1 || strings.Contains(bodies[0], "affects") {
		t.Fatalf("first connection notices %q, want one failing notice without affected features", bodies)
	}

	body := capabilityAPI(t, s.base, token, "/connections")
	var listed struct {
		Data struct {
			Connections []struct {
				ID         string `json:"id"`
				Dependents []struct {
					Module     string   `json:"module"`
					Active     bool     `json:"active"`
					Capability string   `json:"capability"`
					Features   []string `json:"features"`
				} `json:"dependents"`
			} `json:"connections"`
		}
	}
	if err := json.Unmarshal([]byte(body), &listed); err != nil {
		t.Fatal(err)
	}
	for _, c := range listed.Data.Connections {
		if c.ID != "service/grafana-b" {
			continue
		}
		if len(c.Dependents) != 1 || c.Dependents[0].Module != "grafana" || c.Dependents[0].Capability != "viewer" ||
			!c.Dependents[0].Active || !slices.Contains(c.Dependents[0].Features, "server health") {
			t.Fatalf("dependents of the second grafana connection = %+v", c.Dependents)
		}
	}

	secondStatus.Store(http.StatusInternalServerError)
	if got := runApp(t, "connections", "test", "service/grafana-b"); got.code != cli.ExitError {
		t.Fatalf("failing second connection test: exit %d %s", got.code, got.stderr)
	}
	bodies := noticeBodies(t, dir, "Grafana B")
	if len(bodies) != 1 || !strings.Contains(bodies[0], "; affects server health, server tools") {
		t.Fatalf("second connection notices %q, want one failing notice naming the affected features", bodies)
	}
	model = capabilityDependencies(t, s.base, token)
	viewer = model.need(t, "grafana", "viewer")
	if viewer.Connection != "service/grafana" || viewer.Met || viewer.Availability != "unavailable" || viewer.Repair != "connection:service/grafana:unreachable" {
		t.Fatalf("grafana viewer with both connections failing = %+v, want the first, unavailable", viewer)
	}
	for _, id := range []string{"service/grafana", "service/grafana-b"} {
		if r, ok := model.repair(id); !ok || r.Severity != "blocker" || !slices.Equal(r.Features, []string{"server health", "server tools"}) {
			t.Fatalf("repair for %s with the hard need unmet = %+v (found %v)", id, r, ok)
		}
	}

	firstStatus.Store(http.StatusOK)
	if got := runApp(t, "connections", "test", "service/grafana"); got.code != cli.ExitOK {
		t.Fatalf("recovered first connection test: exit %d %s", got.code, got.stderr)
	}
	model = capabilityDependencies(t, s.base, token)
	if viewer = model.need(t, "grafana", "viewer"); viewer.Connection != "service/grafana" || !viewer.Met {
		t.Fatalf("grafana viewer after the first recovered = %+v", viewer)
	}
	if _, ok := model.repair("service/grafana"); ok {
		t.Fatalf("the recovered connection still has a repair: %+v", model.Repairs)
	}
	if r, ok := model.repair("service/grafana-b"); !ok || r.Severity != "notice" || len(r.Features) != 0 {
		t.Fatalf("repair for the still failing second connection = %+v (found %v), want a notice with no features", r, ok)
	}
	if got := runApp(t, "connections", "test", "service/grafana-b"); got.code != cli.ExitError {
		t.Fatalf("second failing test: exit %d", got.code)
	}
	if n := len(noticeBodies(t, dir, "Grafana A")); n != 2 {
		t.Fatalf("%d first connection notices after failing and recovering, want 2", n)
	}
	if n := len(noticeBodies(t, dir, "Grafana B")); n != 1 {
		t.Fatalf("%d second connection notices after an unchanged failing check, want 1", n)
	}
}

func TestMediaChannelsJobSkipsOnAnUnavailableConnectionAndNamesTheRepair(t *testing.T) {
	var mode atomic.Int32
	release := func(letter, title string) map[string]any {
		hash := strings.Repeat(letter, 40)
		return map[string]any{"hash": hash, "magnet_url": mediaMagnet(hash, "Lanterns"), "title": title, "size_bytes": "1000"}
	}
	feed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		switch mode.Load() {
		case 0:
			_ = json.NewEncoder(w).Encode(map[string]any{"torrents": []map[string]any{release("a", "Lanterns S01E01 1080p")}})
		case 1:
			_ = json.NewEncoder(w).Encode(map[string]any{"torrents": []map[string]any{
				release("a", "Lanterns S01E01 1080p"), release("b", "Lanterns S01E02 1080p")}})
		default:
			w.WriteHeader(http.StatusBadGateway)
		}
	}))
	defer feed.Close()
	var adds atomic.Int32
	qbit := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/add") {
			adds.Add(1)
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer qbit.Close()
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	mnpMediaRoot(t, dir)
	setEnv(t, "ATRIUM_HOST_CONFIG", mediaHost(t, dir, qbit.URL, feed.URL+"/api/get-torrents", "", ""))
	d := asstDB(t, dir)
	declareJobsAndParkThem(t, d)

	token := localAPIToken(t, "capabilities")
	s := &connectionSite{t: t, dir: dir}
	s.start()
	b := mnpNewBrowser(t, s.base)
	b.want(http.MethodGet, "/apps/media/channels", nil, http.StatusOK)
	b.want(http.MethodPost, "/apps/media/channels", url.Values{"name": {"Lanterns"}, "imdb": {"tt1234567"}, "pattern": {"1080p"}}, http.StatusOK)
	model := capabilityDependencies(t, s.base, token)
	indexer := model.need(t, "media", "indexer")
	if indexer.Kind != "service/eztv" || indexer.Connection != "service/eztv" || !indexer.Soft || !slices.Contains(indexer.Features, "media channels") {
		t.Fatalf("media indexer need = %+v", indexer)
	}
	if qb := model.need(t, "torrents", "api"); qb.Soft {
		t.Fatalf("torrents qbittorrent api need = %+v, want hard", qb)
	}
	s.stop()

	tick := func() (string, string) {
		t.Helper()
		asstExec(t, d, "UPDATE jobs SET next_due = ? WHERE name = ?", float64(time.Now().Unix())-1, library.JobChannels)
		if ran := runApp(t, "agent", "tick"); ran.code != cli.ExitOK {
			t.Fatalf("agent tick: exit %d %q", ran.code, ran.stderr)
		}
		return schedulerStatus(t, d, library.JobChannels)
	}
	if status, text := tick(); status != "ok" {
		t.Fatalf("priming channels run %s %q, want ok", status, text)
	}
	mode.Store(1)
	status, text := tick()
	if status != "skipped_dependency" || !strings.Contains(text, "repair connection:service/qbittorrent:unreachable") || adds.Load() == 0 {
		t.Fatalf("channels run with every add failing = %s %q after %d adds, want skipped_dependency naming the qBittorrent repair", status, text, adds.Load())
	}
	mode.Store(2)
	status, text = tick()
	if status != "skipped_dependency" || !strings.Contains(text, "indexer from service/eztv is unavailable") ||
		!strings.Contains(text, "repair connection:service/eztv:unreachable at /settings/connections") {
		t.Fatalf("channels run with the feed failing = %s %q, want skipped_dependency naming the EZTV repair", status, text)
	}
	if n := asstCount(t, d, "SELECT COUNT(*) FROM job_runs r JOIN jobs j ON j.id = r.job_id WHERE j.name = ? AND r.status = 'skipped_dependency'", library.JobChannels); n != 2 {
		t.Fatalf("%d skipped_dependency channel runs, want 2 unmerged rows", n)
	}
}

func TestPackageTrackingJobSkipsWithoutShip24WhilePackagesWait(t *testing.T) {
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	fake := &fakeShip24{milestone: "in_transit"}
	srv := httptest.NewServer(http.HandlerFunc(fake.handler))
	t.Cleanup(srv.Close)
	setEnv(t, "ATRIUM_HOST_CONFIG", ship24Host(t, dir, srv.URL+"/public/v1", ""))
	d := asstDB(t, dir)
	declareJobsAndParkThem(t, d)
	tick := func() (string, string) {
		t.Helper()
		asstExec(t, d, "UPDATE jobs SET next_due = ? WHERE name = ?", float64(time.Now().Unix())-1, tracking.JobPackageTracking)
		if ran := runApp(t, "agent", "tick"); ran.code != cli.ExitOK {
			t.Fatalf("agent tick: exit %d %q", ran.code, ran.stderr)
		}
		return schedulerStatus(t, d, tracking.JobPackageTracking)
	}
	if status, text := tick(); status != "ok" {
		t.Fatalf("tracking run with no packages = %s %q, want ok", status, text)
	}
	now := float64(time.Now().Unix())
	asstExec(t, d, "INSERT INTO packages (id, carrier, tracking_number, merchant, merchant_key, order_ref, status, status_text, "+
		"source_message_id, last_message_id, updated_ts, first_seen, aggregator, tracker_id) "+
		"VALUES (1, 'UPS', '1Z999AA10123456784', 'Shop', 'shop', '', 'shipped', 'Shipped', 1, 1, ?, ?, 'ship24', 'trk_1')", now, now)
	status, text := tick()
	if status != "skipped_dependency" || !strings.Contains(text, "api from service/ship24 is unavailable") ||
		!strings.Contains(text, "repair connection:service/ship24:config") {
		t.Fatalf("tracking run without a Ship24 key while a package waits = %s %q", status, text)
	}
	if got := fake.requestCount(); got != 0 {
		t.Fatalf("tracking without a key made %d requests", got)
	}
	if ran := runApp(t, "packages", "poll"); ran.code != cli.ExitOK {
		t.Fatalf("packages poll without a key: exit %d %s, want the unchanged success", ran.code, ran.stderr)
	}
}

func TestHostOwnedConnectionsStayReadOnlyAndTheOwnersCheckFlagsADrift(t *testing.T) {
	requireCipher(t)
	srv, _ := grafanaHealthServer(t)
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	setEnv(t, "ATRIUM_DB_KEY", rightKeyHex)
	setEnv(t, "ATRIUM_HOST_CONFIG", servicesHostConfig(t, dir, srv.URL, "http://anthropic.invalid"))
	opsBase := opsAddr(t)
	s := &connectionSite{t: t, dir: dir}
	s.start()
	defer func() { s.stop() }()
	checks := func() (string, map[string]string) {
		t.Helper()
		_, body, _ := fetch(t, http.DefaultClient, http.MethodGet, opsBase()+"/readyz", nil)
		var got struct {
			Status string
			Checks map[string]string
		}
		if err := json.Unmarshal([]byte(body), &got); err != nil {
			t.Fatalf("readyz body %q: %v", body, err)
		}
		return got.Status, got.Checks
	}
	waitFor(t, func() bool { _, c := checks(); return c["connection owners"] == "ok" })

	key := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
	config := func() string {
		t.Helper()
		var raw, source string
		if err := key.Read.QueryRow("SELECT config_json, config_source FROM connections WHERE id = 'service/grafana'").Scan(&raw, &source); err != nil {
			t.Fatal(err)
		}
		if source != "host" {
			t.Fatalf("grafana config source %q, want host", source)
		}
		return raw
	}
	before := config()
	s.conn("/test", url.Values{"id": {"service/grafana"}, "url": {"http://elsewhere.invalid"}, "api_key": {"glsa_other"}})
	if after := config(); after != before || strings.Contains(after, "elsewhere") {
		t.Fatalf("a UI test changed the host-owned config from %s to %s", before, after)
	}

	if _, err := key.Write.Exec("UPDATE connections SET config_source = 'operator' WHERE id = 'service/grafana'"); err != nil {
		t.Fatal(err)
	}
	status, c := checks()
	if status != string(ops.StateDegraded) || !strings.Contains(c["connection owners"], "service/grafana") {
		t.Fatalf("readyz after an ownership drift = %s %q, want degraded naming service/grafana", status, c["connection owners"])
	}
}

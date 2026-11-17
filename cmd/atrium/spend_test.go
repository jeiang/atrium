package main

import (
	"math"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/assistant/pricing"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/inference"
)

func TestSpendOneRunCapRefusesEveryModelBeforeDispatch(t *testing.T) {
	t.Parallel()
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	asstWarm(t)
	d := asstDB(t, dir)
	asstExec(t, d, "INSERT INTO settings (key, value) VALUES ('max_run_cost_usd_haiku_5_5', '0.0001')")
	setEnv(t, "ATRIUM_ASSISTANT_MAX_RUN_COST_USD_SONNET_5_5", "0.0001")

	listed := runAppLogged(t, "settings", "list")
	if listed.code != cli.ExitOK || !strings.Contains(listed.stderr, "ignoring a removed setting") || !strings.Contains(listed.stderr, "ATRIUM_ASSISTANT_MAX_RUN_COST_USD_SONNET_5_5") {
		t.Fatalf("settings list with a retired run cap pinned: exit %d stderr %q", listed.code, listed.stderr)
	}
	if strings.Contains(listed.stdout, "max_run_cost_usd_") || !strings.Contains(listed.stdout, "max_run_cost_usd") {
		t.Fatalf("settings list shows the per-model caps or lacks the one cap: %s", listed.stdout)
	}
	if ran := runApp(t, "settings", "set", "max_run_cost_usd_opus_5_5", "1"); ran.code == cli.ExitOK {
		t.Fatalf("a retired per-model cap was written: %q", ran.stdout)
	}

	models := []string{pricing.HaikuID, pricing.SonnetID, pricing.OpusID}
	for _, model := range models {
		asstSet(t, "default_model", model)
		api.Push(asstReply("ok"))
		sent := len(api.Requests())
		if ran, _ := asstRun(t, "--text", "hi"); ran.State != "done" || len(api.Requests()) != sent+1 {
			t.Fatalf("%s run with retired caps stored and pinned: %+v %q, %d requests", model, ran, ran.err(), len(api.Requests())-sent)
		}
	}

	asstSet(t, inference.RunCapKey, "0.0001")
	for _, model := range models {
		asstSet(t, "default_model", model)
		sent := len(api.Requests())
		rows := asstCount(t, d, "SELECT COUNT(*) FROM usage")
		refused, _ := asstRun(t, "--text", "hi")
		if refused.State != "failed" || !strings.Contains(refused.err(), "run cost cap") || !strings.Contains(refused.err(), "raise max_run_cost_usd to continue") {
			t.Fatalf("%s run past the one cap: %+v %q", model, refused, refused.err())
		}
		if len(api.Requests()) != sent || asstCount(t, d, "SELECT COUNT(*) FROM usage") != rows {
			t.Fatalf("%s run past the cap reached the provider or reserved: %d requests", model, len(api.Requests())-sent)
		}
	}
}

type spendRow struct {
	ts        float64
	month     string
	class     string
	kind      string
	model     string
	cost      float64
	committed int64
}

func spendRows(t *testing.T, dir string) []spendRow {
	t.Helper()
	rows, err := asstDB(t, dir).Read.Query("SELECT ts, month, budget_class, kind, model, cost_usd, state, reservation_nusd, accounted_nusd FROM usage")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []spendRow
	for rows.Next() {
		var r spendRow
		var state string
		var reserved, accounted int64
		if err := rows.Scan(&r.ts, &r.month, &r.class, &r.kind, &r.model, &r.cost, &state, &reserved, &accounted); err != nil {
			t.Fatal(err)
		}
		r.committed = accounted
		if state == "reserved" || state == "unknown" {
			r.committed = reserved
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func nearly(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func checkSpendSums(t *testing.T, body map[string]any, rows []spendRow, loc *time.Location, month string) {
	t.Helper()
	var calls int64
	var cost float64
	var committed int64
	days := map[string][3]float64{}
	models := map[string]float64{}
	for _, r := range rows {
		if r.month != month || r.class == "subscription" {
			continue
		}
		calls++
		cost += r.cost
		committed += r.committed
		sec, frac := math.Modf(r.ts)
		date := time.Unix(int64(sec), int64(frac*1e9)).In(loc).Format(time.DateOnly)
		day := days[date]
		days[date] = [3]float64{day[0] + 1, day[1] + r.cost, day[2] + float64(r.committed)/inference.NanoUSD}
		models[r.model] += r.cost
	}
	budget := body["budget"].(map[string]any)
	if !nearly(budget["committed_usd"].(float64), float64(committed)/inference.NanoUSD) {
		t.Errorf("committed %v, usage rows sum to %v", budget["committed_usd"], float64(committed)/inference.NanoUSD)
	}
	for _, split := range []string{"by_class", "by_kind", "by_model"} {
		var c int64
		var usd, held float64
		for _, item := range body[split].([]any) {
			s := item.(map[string]any)
			c += number(t, s["calls"])
			usd += s["cost_usd"].(float64)
			held += s["committed_usd"].(float64)
		}
		if c != calls || !nearly(usd, cost) || !nearly(held, float64(committed)/inference.NanoUSD) {
			t.Errorf("%s sums to %d calls $%v ($%v committed), usage rows to %d $%v ($%v)", split, c, usd, held, calls, cost, float64(committed)/inference.NanoUSD)
		}
	}
	for _, item := range body["by_model"].([]any) {
		s := item.(map[string]any)
		if !nearly(s["cost_usd"].(float64), models[s["key"].(string)]) {
			t.Errorf("model %v costs %v, usage rows %v", s["key"], s["cost_usd"], models[s["key"].(string)])
		}
	}
	byDay := body["by_day"].([]any)
	if first := byDay[0].(map[string]any)["date"].(string); first != month+"-01" {
		t.Errorf("by_day starts at %s, want the first of %s", first, month)
	}
	for _, item := range byDay {
		s := item.(map[string]any)
		want := days[s["date"].(string)]
		if float64(number(t, s["calls"])) != want[0] || !nearly(s["cost_usd"].(float64), want[1]) || !nearly(s["committed_usd"].(float64), want[2]) {
			t.Errorf("day %v: %v calls $%v ($%v), usage rows in the configured zone give %v", s["date"], s["calls"], s["cost_usd"], s["committed_usd"], want)
		}
	}
}

func spendCap(t *testing.T, body map[string]any, key string) map[string]any {
	t.Helper()
	for _, item := range body["caps"].([]any) {
		if c := item.(map[string]any); c["key"] == key {
			return c
		}
	}
	t.Fatalf("spend caps lack %s: %v", key, body["caps"])
	return nil
}

func TestSpendModelSumsUsageInTheConfiguredZoneAndShowsExhaustion(t *testing.T) {
	t.Parallel()
	dir, api, f := moduleFixture(t, flagsExcept("", ""))
	asstSet(t, "tz", "Asia/Tokyo")
	asstWarm(t)
	loc, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().In(loc)
	start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, loc)
	month := start.Format("2006-01")
	d := asstDB(t, dir)
	var run, thread int64
	if err := d.Read.QueryRow("SELECT id, thread_id FROM runs ORDER BY id LIMIT 1").Scan(&run, &thread); err != nil {
		t.Fatal(err)
	}
	stamp := func(at time.Time) float64 { return float64(at.UnixMicro()) / 1e6 }
	far := stamp(now.Add(240 * time.Hour))
	insert := "INSERT INTO usage (attempt_key, ts, month, expires_at, settled_at, state, budget_class, reservation_nusd, accounted_nusd, source, provider, kind, model, effort, cost_usd, run_id, thread_id) " +
		"VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'loop', 'anthropic', ?, ?, 'medium', ?, ?, ?)"
	before := start.Add(-time.Hour)
	asstExec(t, d, insert, "spend-before", stamp(before), before.Format("2006-01"), far, stamp(before), "settled", "interactive", 5_000_000_000, 5_000_000_000, "chat", pricing.OpusID, 5.0, nil, nil)
	early := start.Add(5 * time.Hour)
	asstExec(t, d, insert, "spend-early", stamp(early), month, far, stamp(early), "settled", "unattended", 400_000_000, 400_000_000, "nightly", pricing.SonnetID, 0.4, run, thread)
	second := start.Add(27 * time.Hour)
	asstExec(t, d, insert, "spend-held", stamp(second), month, far, nil, "reserved", "interactive", 250_000_000, 0, "chat", pricing.OpusID, 0.0, nil, nil)

	token := localAPIToken(t, "spend-api")
	base, _, stop, err := startServeAppArgs(t, f.app, &lockedBuffer{}, "--auth", "local")
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	s := &apiSite{t: t, base: base}

	got := s.do(http.MethodGet, "/api/v1/spend", token, nil)
	if got.status != 200 || got.header.Get("ETag") == "" {
		t.Fatalf("spend: %d %s", got.status, got.body)
	}
	if got.json["month"] != month || got.json["state"] != "ok" {
		t.Fatalf("spend month %v state %v, want %s ok", got.json["month"], got.json["state"], month)
	}
	checkSpendSums(t, got.json, spendRows(t, dir), loc, month)
	top := got.json["top_runs"].([]any)[0].(map[string]any)
	if number(t, top["run_id"]) != run || number(t, top["thread_id"]) != thread || top["cost_usd"].(float64) < 0.4 {
		t.Errorf("largest run %v, want run %d in thread %d with the seeded $0.40", top, run, thread)
	}
	if runCap := spendCap(t, got.json, inference.RunCapKey); runCap["value"] != float64(pricing.DefaultRunCapUSD) || runCap["source"] != "default" || runCap["editable"] != true {
		t.Errorf("run cap entry %v", runCap)
	}
	if web := s.do(http.MethodGet, "/web/v1/spend", token, nil); web.status == 200 {
		t.Errorf("/web/v1/spend answered a bearer request: %d", web.status)
	}

	if again := s.do(http.MethodGet, "/api/v1/spend", token, nil, "If-None-Match", got.header.Get("ETag")); again.status != 304 {
		t.Fatalf("unchanged spend: %d, want 304", again.status)
	}
	api.Push(asstReply("ok"))
	if ran, _ := asstRun(t, "--text", "hi"); ran.State != "done" {
		t.Fatalf("run recording cost: %+v %q", ran, ran.err())
	}
	moved := s.do(http.MethodGet, "/api/v1/spend", token, nil, "If-None-Match", got.header.Get("ETag"))
	if moved.status != 200 || moved.header.Get("ETag") == got.header.Get("ETag") {
		t.Fatalf("spend after a run: %d, want a new revision", moved.status)
	}
	checkSpendSums(t, moved.json, spendRows(t, dir), loc, month)

	budget := moved.json["budget"].(map[string]any)
	used := budget["committed_usd"].(float64) + budget["carry_in_usd"].(float64)
	asstSet(t, inference.BudgetKey, strconv.FormatFloat(used/0.87, 'f', -1, 64))
	near := s.do(http.MethodGet, "/api/v1/spend", token, nil)
	if near.json["state"] != "near" {
		t.Fatalf("spend with scheduled work paused: state %v, %s", near.json["state"], near.body)
	}
	asstSet(t, inference.BudgetKey, strconv.FormatFloat(used/2, 'f', -1, 64))
	exhausted := s.do(http.MethodGet, "/api/v1/spend", token, nil)
	if exhausted.json["state"] != "exhausted" {
		t.Fatalf("spend past the budget: state %v", exhausted.json["state"])
	}
	for _, item := range exhausted.json["headroom"].([]any) {
		h := item.(map[string]any)
		if h["class"] != "classification" && (h["refused"] != true || h["headroom_usd"] != float64(0)) {
			t.Errorf("headroom %v, want chat and scheduled work refused", h)
		}
	}
	if budgetCap := spendCap(t, exhausted.json, inference.BudgetKey); budgetCap["source"] != "db" || budgetCap["editable"] != true || !nearly(budgetCap["value"].(float64), used/2) {
		t.Errorf("budget cap entry %v", budgetCap)
	}
	sent := len(api.Requests())
	refused := runApp(t, "agent", "run", "--text", "hi")
	if refused.code == cli.ExitOK || !strings.Contains(refused.stdout+refused.stderr, "monthly budget") {
		t.Fatalf("run with the budget exhausted: exit %d stdout %q stderr %q", refused.code, refused.stdout, refused.stderr)
	}
	if len(api.Requests()) != sent {
		t.Fatalf("run with the budget exhausted reached the provider: %d requests", len(api.Requests())-sent)
	}
}

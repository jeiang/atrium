package service_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/assistant/budget"
	actx "github.com/jeiang/atrium/internal/assistant/context"
	"github.com/jeiang/atrium/internal/assistant/journal"
	"github.com/jeiang/atrium/internal/assistant/loop"
	"github.com/jeiang/atrium/internal/assistant/prompts"
	"github.com/jeiang/atrium/internal/assistant/service"
	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/assistant/tool"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/testutil/golden"
)

const now0 = 1_780_000_000.0

func eqJSON(t *testing.T, want, got any) {
	t.Helper()
	w, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	g, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	golden.EqualJSON(t, w, g)
}

var writeCalls []py.Dict

func loopTools(t *testing.T) []tool.Tool {
	writeCalls = nil
	big := strings.Repeat("x", actx.StubTokens*actx.CharsPerToken+40)
	return []tool.Tool{
		probe("probe", tool.Read, func(ctx context.Context, tc *tool.Context, args py.Dict) (any, error) {
			id, _ := args.Get("id")
			call, err := journal.GetToolCall(ctx, tc.DB.Read, tc.RunID, id.(string))
			if err != nil || call == nil || call.State != journal.Started {
				t.Errorf("probe ran before its call was journaled: %+v %v", call, err)
			}
			x, _ := args.Get("x")
			return py.Dict{{Key: "echo", Value: x}, {Key: "journaled", Value: true}}, nil
		}),
		probe("big", tool.Read, func(context.Context, *tool.Context, py.Dict) (any, error) { return big, nil }),
		probe("broken", tool.Read, func(context.Context, *tool.Context, py.Dict) (any, error) { return nil, tool.Error("nope") }),
		probe("note_write", tool.Write, func(_ context.Context, _ *tool.Context, args py.Dict) (any, error) {
			writeCalls = append(writeCalls, args)
			return py.Dict{{Key: "ok", Value: true}}, nil
		}),
	}
}

func TestLoopExecutesToolsJournalsIntentAndReturnsThinkingUnchanged(t *testing.T) {
	w := newWorld(t, loopTools(t)...)
	think := anthropictest.Thinking("deep", "signed-1")
	w.push(
		toolTurn(think, use("tu1", "probe", map[string]any{"id": "tu1", "x": 7}), use("tu2", "broken", nil)),
		reply(text("All done.")),
	)
	thread, run := w.chat("hello", threads.Chat)
	found := w.run(run)
	if found.State != journal.Done || found.Steps != 2 {
		t.Fatalf("run %+v", found)
	}
	second := w.body(1)
	golden.EqualJSON(t, think, mustJSON(t, path(second, "messages", 1, "content", 0)))
	eqJSON(t, map[string]any{"type": "tool_result", "tool_use_id": "tu2", "content": "nope", "is_error": true, "cache_control": map[string]any{"type": "ephemeral"}},
		path(second, "messages", 2, "content", 1))
	calls, err := journal.ToolCallsOf(t.Context(), w.d.Read, run)
	if err != nil {
		t.Fatal(err)
	}
	var got [][3]string
	for _, c := range calls {
		got = append(got, [3]string{c.Name, c.State, c.ReplayClass})
	}
	if want := [][3]string{{"probe", journal.OK, journal.Safe}, {"broken", journal.Error, journal.Safe}}; !slices.Equal(got, want) {
		t.Errorf("calls %v, want %v", got, want)
	}
	stored := w.messages(thread)
	var roles []string
	for _, m := range stored {
		roles = append(roles, m.Role)
	}
	if !slices.Equal(roles, []string{"user", "assistant", "user", "assistant"}) {
		t.Errorf("roles %v", roles)
	}
	golden.EqualJSON(t, think, stored[1].Content[0])
	if title := w.threadRow(thread).Title; title == nil || *title != "hello" {
		t.Errorf("title %v", title)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestJournalKeepsProductionPythonBlockKeyOrder(t *testing.T) {
	w := newWorld(t, loopTools(t)...)
	w.push(toolTurn(anthropictest.Thinking("deep", "s1"), use("tu1", "probe", map[string]any{"id": "tu1", "x": 1})), reply(text("ok")))
	thread, _ := w.chat("hello", threads.Chat)
	got := w.messages(thread)[1].Content
	want := []string{`{"signature":"s1","thinking":"deep","type":"thinking"}`, `{"id":"tu1","input":{"id":"tu1","x":1},"name":"probe","type":"tool_use"}`}
	for i, raw := range got {
		if string(raw) != want[i] {
			t.Errorf("block %d journaled as %s, want %s", i, raw, want[i])
		}
	}
}

func TestRequestShapePinsEffortCachingAndStamp(t *testing.T) {
	w := newWorld(t, loopTools(t)...)
	w.push(reply(text("hi")))
	thread, _ := w.chat("see https://example.test/a, please", threads.Chat)
	params := w.body(0)
	eqJSON(t, map[string]any{"type": "adaptive", "display": "summarized"}, params["thinking"])
	if params["model"] != loop.Model || params["max_tokens"] != float64(loop.MaxTokens) {
		t.Errorf("model %v max_tokens %v", params["model"], params["max_tokens"])
	}
	eqJSON(t, map[string]any{"effort": "medium"}, params["output_config"])
	if path(params, "system", 0, "text") != prompts.System {
		t.Error("system[0] is not prompts.System")
	}
	eqJSON(t, map[string]any{"type": "ephemeral"}, path(params, "system", 0, "cache_control"))
	tools, _ := params["tools"].([]any)
	var names []string
	for _, tl := range tools {
		names = append(names, path(tl, "name").(string))
	}
	if !slices.IsSorted(names[:len(names)-1]) || names[len(names)-1] != "web_search" {
		t.Errorf("tool order %v", names)
	}
	last := tools[len(tools)-1]
	if path(last, "type") != "web_search_20260318" || path(last, "max_uses") != 5.0 || path(last, "cache_control") == nil {
		t.Errorf("web_search %v", last)
	}
	for _, tl := range tools[:len(tools)-1] {
		if path(tl, "cache_control") != nil {
			t.Errorf("tool %v carries a breakpoint", path(tl, "name"))
		}
	}
	user := path(params, "messages", 0, "content")
	if path(user, 0, "text") != "see https://example.test/a, please" {
		t.Errorf("user text %v", path(user, 0, "text"))
	}
	if stamp, _ := path(user, 1, "text").(string); !strings.HasPrefix(stamp, "Current date and time:") || path(user, 1, "cache_control") == nil {
		t.Errorf("stamp block %v", path(user, 1))
	}
	if urls := w.threadRow(thread).AllowedURLs; len(urls) != 0 {
		t.Errorf("model-visible text allowlisted %v", urls)
	}
	if strings.Contains(strings.ToLower(path(params, "system", 0, "text").(string)), "timestamp") {
		t.Error("timestamp in the system prompt")
	}
}

func TestOnlyOperatorInputAllowlistsURLs(t *testing.T) {
	w := newWorld(t)
	typed := w.thread(threads.Chat)
	w.push(reply(text("hi")))
	if _, err := w.svc.RunOnce(t.Context(), typed, "see https://example.test/a, please", service.Start{Operator: true}); err != nil {
		t.Fatal(err)
	}
	if urls := w.threadRow(typed).AllowedURLs; !slices.Equal(urls, []string{"https://example.test/a"}) {
		t.Errorf("operator urls %v", urls)
	}
	for _, kind := range []string{threads.Chat, threads.Cron, threads.Sub} {
		w.push(reply(text("hi")))
		thread, _ := w.chat("open https://attacker.test/?d=x", kind)
		if urls := w.threadRow(thread).AllowedURLs; len(urls) != 0 {
			t.Errorf("%s: %v", kind, urls)
		}
	}
}

func TestCronRunsAtHighEffortWithoutWebSearchForNightly(t *testing.T) {
	w := newWorld(t, loopTools(t)...)
	w.push(reply(text("ok")), reply(text("ok")))
	w.chat("tick", threads.Cron)
	w.chat("tick", threads.Nightly)
	cron, nightly := w.body(0), w.body(1)
	eqJSON(t, map[string]any{"effort": "high"}, cron["output_config"])
	if path(cron, "tools", -1, "name") != "web_search" {
		t.Error("cron lacks web_search")
	}
	for _, tl := range nightly["tools"].([]any) {
		if path(tl, "type") == "web_search_20260318" {
			t.Error("nightly offers web_search")
		}
	}
}

func TestWebSearchIsDirectOnlyInRequestBody(t *testing.T) {
	for _, kind := range []string{threads.Chat, threads.Cron} {
		w := newWorld(t, loopTools(t)...)
		w.push(reply(text("ok")))
		w.chat("q", kind)
		var found []any
		for _, tl := range w.body(0)["tools"].([]any) {
			if path(tl, "name") == "web_search" {
				found = append(found, tl)
			}
		}
		if len(found) != 1 {
			t.Fatalf("%s: %d web_search tools", kind, len(found))
		}
		eqJSON(t, []string{"direct"}, path(found[0], "allowed_callers"))
	}
}

func TestWebSearchURLsBecomeAllowed(t *testing.T) {
	w := newWorld(t)
	blocks := append(anthropictest.WebSearch("srv1", "weather", "https://a.test/1", "https://b.test/2"), text("Found."))
	w.push(reply(blocks...).WithUsage(anthropictest.Usage{Input: 1000, Output: 100, Searches: 1}))
	thread, run := w.chat("hello", threads.Chat)
	if urls := w.threadRow(thread).AllowedURLs; !slices.Equal(urls, []string{"https://a.test/1", "https://b.test/2"}) {
		t.Errorf("allowed %v", urls)
	}
	var searches int64
	var cost float64
	if err := w.d.Read.QueryRow("SELECT searches, cost_usd FROM usage WHERE run_id = ?", run).Scan(&searches, &cost); err != nil {
		t.Fatal(err)
	}
	if searches != 1 || cost <= 0.01 {
		t.Errorf("searches %d cost %v", searches, cost)
	}
}

func TestLargeResultsAreStubbed(t *testing.T) {
	w := newWorld(t, loopTools(t)...)
	w.push(toolTurn(use("tu1", "big", nil)), reply(text("done")))
	_, run := w.chat("hello", threads.Chat)
	var stub map[string]any
	if err := json.Unmarshal([]byte(path(w.body(1), "messages", 2, "content", 0, "content").(string)), &stub); err != nil {
		t.Fatal(err)
	}
	big := strings.Repeat("x", actx.StubTokens*actx.CharsPerToken+40)
	if stub["result_id"] != 1.0 || stub["tool"] != "big" || stub["tokens"].(float64) <= actx.StubTokens || stub["preview"] != big[:actx.PreviewChars] {
		t.Errorf("stub %v", stub)
	}
	var body string
	if err := w.d.Read.QueryRow("SELECT body FROM tool_results WHERE result_id = 1").Scan(&body); err != nil || body != big {
		t.Errorf("stored body: %v", err)
	}
	call, err := journal.GetToolCall(t.Context(), w.d.Read, run, "tu1")
	if err != nil || call == nil || call.ResultID == nil || *call.ResultID != 1 {
		t.Errorf("call %+v %v", call, err)
	}
}

func TestDispatchRefusesToolsTheRunDoesNotOffer(t *testing.T) {
	sub := probe("sub_only", tool.Read, echo)
	sub.Contexts = tool.Kinds{tool.Sub}
	failing := probe("crash", tool.Read, func(context.Context, *tool.Context, py.Dict) (any, error) {
		return nil, errors.New("boom")
	})
	w := newWorld(t, append(loopTools(t), sub, failing)...)
	w.push(
		toolTurn(use("a", "sub_only", nil), use("b", "nonexistent", nil), use("c", "crash", nil)),
		reply(text("ok")),
	)
	_, run := w.chat("go", threads.Chat)
	results := path(w.body(1), "messages", 2, "content").([]any)
	want := []string{"unknown tool sub_only", "unknown tool nonexistent", "crash failed: RuntimeError"}
	for i, r := range results {
		if path(r, "content") != want[i] || path(r, "is_error") != true {
			t.Errorf("result %d: %v", i, r)
		}
	}
	calls, _ := journal.ToolCallsOf(t.Context(), w.d.Read, run)
	if len(calls) != 3 || calls[0].ReplayClass != journal.Safe || calls[0].State != journal.Error {
		t.Errorf("calls %+v", calls)
	}
}

func TestRunStopsAtMaxStepsAndCostCeiling(t *testing.T) {
	w := newWorld(t, loopTools(t)...)
	for i := range 5 {
		id := "t" + string(rune('0'+i))
		w.push(toolTurn(use(id, "probe", map[string]any{"id": id})))
	}
	thread := w.thread(threads.Chat)
	w.svc.Now = clock(now0)
	run, err := w.svc.Prepare(t.Context(), thread, "go", now0, service.Start{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.Execute(t.Context(), run, journal.Limits{MaxSteps: new(int64(3))}); err != nil {
		t.Fatal(err)
	}
	found := w.run(run)
	if found.State != journal.Failed || found.Error == nil || *found.Error != "stopped after 3 steps" || len(w.srv.Requests()) != 3 {
		t.Fatalf("run %+v error %v, %d requests", found, deref(found.Error), len(w.srv.Requests()))
	}

	w = newWorld(t, loopTools(t)...)
	w.push(toolTurn(use("a", "probe", map[string]any{"id": "a"})).WithUsage(anthropictest.Usage{Input: 1000, Output: 4_000_000}), reply(text("x")))
	thread = w.thread(threads.Chat)
	w.svc.Now = clock(now0)
	run, err = w.svc.Prepare(t.Context(), thread, "go", now0, service.Start{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.Execute(t.Context(), run, journal.Limits{MaxCostUSD: new(1.0)}); err != nil {
		t.Fatal(err)
	}
	if e := deref(w.run(run).Error); !strings.HasPrefix(e, "run cost ceiling reached ($") || !strings.HasSuffix(e, " of $1.00)") || len(w.srv.Requests()) != 1 {
		t.Errorf("error %q, %d requests", e, len(w.srv.Requests()))
	}
}

func deref(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

func TestTimeStampFollowsAtriumTZ(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Fatal(err)
	}
	moment := wall(2026, time.October, 31, 20, 0)
	if got := loop.TimeStamp(moment, loc); got != "Current date and time: Sunday 2026-11-01 09:00 JST" {
		t.Errorf("stamp %q", got)
	}
}

// spend is the Python test's spend: one finished run with a usage row of cost.
func spend(t *testing.T, d *db.DB, cost, ts float64) {
	t.Helper()
	for _, stmt := range []string{
		"INSERT INTO threads (kind, effort, created_at, updated_at) VALUES ('chat', 'medium', ?1, ?1)",
		"INSERT INTO runs (thread_id, kind, model, effort, state, started_at) VALUES (1, 'chat', 'm', 'medium', 'done', ?1)",
	} {
		if _, err := d.Write.Exec(stmt, ts); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := d.Write.Exec("INSERT INTO usage (ts, thread_id, run_id, kind, model, effort, input_tokens, cache_write_5m, cache_write_1h, "+
		"cache_read, output_tokens, searches, tier, cost_usd) VALUES (?, 1, 1, 'chat', 'm', 'medium', 0, 0, 0, 0, 0, 0, '<=100K', ?)", ts, cost); err != nil {
		t.Fatal(err)
	}
}

func TestBudgetRefusesRunsBeforeCallingTheModel(t *testing.T) {
	w := newWorld(t)
	spend(t, w.d, 60, now0)
	w.push(reply(text("never")))
	cron := w.thread(threads.Cron)
	w.svc.Now = clock(now0)
	_, err := w.svc.RunOnce(t.Context(), cron, "tick", service.Start{Kind: threads.Cron})
	if want := budget.BudgetExceeded("monthly budget: $60.00 of $75.00 spent (80%); unattended runs pause at 80%"); err != want {
		t.Fatalf("cron: %v, want %v", err, want)
	}
	if runs, _ := journal.RunsOf(t.Context(), w.d.Read, cron); len(runs) != 0 {
		t.Errorf("a refused run was created: %+v", runs)
	}
	chat := w.thread(threads.Chat)
	w.turn(chat, "hi", threads.Chat)
	if runs, _ := journal.RunsOf(t.Context(), w.d.Read, chat); len(w.srv.Requests()) != 1 || len(runs) != 1 || runs[0].State != journal.Done {
		t.Errorf("chat runs %+v", runs)
	}
	sub, err := service.CreateThread(t.Context(), w.d.Write, now0, threads.NewThread{Kind: threads.Sub, ParentID: &cron})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.RunOnce(t.Context(), sub, "look", service.Start{Kind: threads.Sub}); !errors.As(err, new(budget.BudgetExceeded)) {
		t.Errorf("sub of a cron thread: %v", err)
	}
}

func TestBudgetExhaustedMidRunFailsTheRunWithTheBudgetText(t *testing.T) {
	var d *db.DB
	spender := probe("spender", tool.Read, func(context.Context, *tool.Context, py.Dict) (any, error) {
		spend(t, d, 80, now0)
		return "spent", nil
	})
	w := newWorld(t, spender)
	d = w.d
	w.push(toolTurn(use("s", "spender", nil)), reply(text("never")))
	_, run := w.chat("go", threads.Chat)
	found := w.run(run)
	if found.State != journal.Failed || deref(found.Error) != "monthly budget: $80.00 of $75.00 spent (107%); chat pauses at 100%" {
		t.Errorf("run %s %q", found.State, deref(found.Error))
	}
	if n := len(w.srv.Requests()); n != 1 {
		t.Errorf("%d requests after the budget ran out", n)
	}
	events := runEvents(t, w.d, run)
	if last := events[len(events)-1]; last.kind != "run_done" || last.data != `{"state": "failed", "error": "monthly budget: $80.00 of $75.00 spent (107%); chat pauses at 100%"}` {
		t.Errorf("last event %+v", last)
	}
}

func TestMissingKeyFailsTheRunAsNotConfigured(t *testing.T) {
	w := newWorld(t)
	w.svc.Client = nil
	thread := w.thread(threads.Chat)
	run, err := w.svc.RunOnce(t.Context(), thread, "hello", service.Start{})
	if err != nil {
		t.Fatal(err)
	}
	if found := w.run(run); found.State != journal.Failed || deref(found.Error) != "ANTHROPIC_API_KEY is not set" {
		t.Errorf("run %s %q", found.State, deref(found.Error))
	}
}

type event struct{ kind, data string }

func runEvents(t *testing.T, d *db.DB, run int64) []event {
	t.Helper()
	rows, err := d.Read.Query("SELECT kind, data_json FROM run_events WHERE run_id = ? ORDER BY id", run)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []event
	for rows.Next() {
		var e event
		if err := rows.Scan(&e.kind, &e.data); err != nil {
			t.Fatal(err)
		}
		out = append(out, e)
	}
	return out
}

func TestProfileBlockIsASeparateCachedSystemBlock(t *testing.T) {
	w := newWorld(t)
	w.runner.Profile = func(context.Context, db.Querier) (string, error) {
		return "Operator profile:\n- city: Port of Spain", nil
	}
	w.runner.SkillsIndex = func(context.Context, db.Querier) (string, error) { return "Skills: wiki-capture", nil }
	w.push(reply(text("hi")))
	w.chat("hello", threads.Chat)
	system := w.body(0)["system"].([]any)
	if len(system) != 3 || !strings.Contains(path(system, 1, "text").(string), "city: Port of Spain") || path(system, 1, "cache_control") != nil {
		t.Fatalf("system %v", system)
	}
	if !strings.Contains(path(system, 2, "text").(string), "wiki-capture") {
		t.Error("skills index missing")
	}
	eqJSON(t, map[string]any{"type": "ephemeral"}, path(system, 2, "cache_control"))
}

func TestThinkingFromADifferentSystemPromptIsDropped(t *testing.T) {
	w := newWorld(t)
	profile := ""
	w.runner.Profile = func(context.Context, db.Querier) (string, error) { return profile, nil }
	w.push(
		reply(anthropictest.Thinking("one", "sig-1"), text("first")),
		reply(anthropictest.Thinking("two", "sig-2"), text("second")),
		reply(text("third")),
	)
	thread := w.thread(threads.Chat)
	w.turn(thread, "hello", threads.Chat)
	profile = "Operator profile:\n- tone: brief"
	w.turn(thread, "again", threads.Chat)
	w.turn(thread, "and again", threads.Chat)
	signatures := func(i int) []string {
		var out []string
		for _, m := range w.body(i)["messages"].([]any) {
			if path(m, "role") != "assistant" {
				continue
			}
			for _, b := range path(m, "content").([]any) {
				if path(b, "type") == "thinking" {
					out = append(out, path(b, "signature").(string))
				}
			}
		}
		return out
	}
	if got := signatures(1); len(got) != 0 {
		t.Errorf("thinking under the old prompt kept: %v", got)
	}
	if got := signatures(2); !slices.Equal(got, []string{"sig-2"}) {
		t.Errorf("thinking under the same prompt: %v", got)
	}
}

func TestThreadViewItemsAndCost(t *testing.T) {
	w := newWorld(t)
	w.push(reply(text("Rayleigh scattering explains it.")))
	thread, _ := w.chat("why is the sky blue", threads.Chat)
	view, err := service.View(t.Context(), w.d.Read, thread)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, i := range view.Items {
		kinds = append(kinds, i.Kind)
	}
	if !slices.Equal(kinds, []string{"user", "assistant"}) || view.Cost <= 0 {
		t.Errorf("items %v cost %v", kinds, view.Cost)
	}
}

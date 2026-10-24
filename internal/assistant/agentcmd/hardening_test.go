package agentcmd

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"math"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jeiang/atrium/internal/anthropic"
	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/assistant/pricing"
	"github.com/jeiang/atrium/internal/assistant/registry"
	"github.com/jeiang/atrium/internal/assistant/tool"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/settings/settingscmd"
)

type harness struct {
	t   *testing.T
	srv *anthropictest.Server
	app *cli.App
	d   *db.DB

	mu     sync.Mutex
	bodies []map[string]any
}

func newHarness(t *testing.T, script ...anthropictest.Response) *harness {
	t.Helper()
	srv := anthropictest.New(t, script...)
	dir := t.TempDir()
	path := filepath.Join(dir, "atrium.db")
	t.Setenv("ATRIUM_DB", path)
	t.Setenv("ATRIUM_CACHE_DIR", filepath.Join(dir, "cache"))
	t.Setenv("ATRIUM_MODE", "shadow")
	env := config.Map(map[string]string{
		"ATRIUM_DB": path, "ATRIUM_CACHE_DIR": filepath.Join(dir, "cache"), "ATRIUM_MODE": "shadow",
		"ANTHROPIC_API_KEY": anthropictest.APIKey, anthropic.BaseURLEnv: srv.URL,
	})
	tools := func() (*registry.Registry, error) {
		r := registry.New()
		err := r.Add(tool.Tool{
			Name: "probe", Description: "Return a fixed result", InputSchema: json.RawMessage(`{"type":"object","properties":{"n":{"type":"integer"}}}`),
			Effect: tool.Read, Contexts: tool.Kinds{tool.Chat, tool.Cron},
			Handler: func(context.Context, *tool.Context, py.Dict) (any, error) { return "same", nil },
		})
		return r, err
	}
	a := cli.New("atrium", func(io.Writer) {})
	a.Add(&cli.Command{Name: "agent", Commands: []*cli.Command{runCommand(env, tools)}}, settingscmd.Command(nil))
	d, err := db.Connect(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	h := &harness{t: t, srv: srv, app: a, d: d}
	srv.OnRequest = func(r anthropictest.Request) {
		var body map[string]any
		if err := json.Unmarshal(r.Body, &body); err != nil {
			t.Errorf("request body: %v", err)
			return
		}
		h.mu.Lock()
		h.bodies = append(h.bodies, body)
		h.mu.Unlock()
	}
	return h
}

func (h *harness) run(args ...string) (map[string]any, string) {
	h.t.Helper()
	var stdout, stderr bytes.Buffer
	if code := h.app.Run(h.t.Context(), args, &stdout, &stderr); code != cli.ExitOK {
		h.t.Fatalf("%v: exit %d: %s", args, code, stderr.String())
	}
	if args[0] != "agent" {
		return nil, stdout.String()
	}
	var view map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &view); err != nil {
		h.t.Fatalf("%v: %v: %s", args, err, stdout.String())
	}
	return view, stdout.String()
}

func (h *harness) set(key, value string) {
	h.t.Helper()
	h.run("settings", "set", key, value)
}

func (h *harness) lastAssistantText(threadID any) string {
	h.t.Helper()
	var content string
	err := h.d.Read.QueryRow("SELECT content_json FROM thread_messages WHERE thread_id = ? AND role = 'assistant' ORDER BY seq DESC LIMIT 1", threadID).Scan(&content)
	if err != nil {
		h.t.Fatal(err)
	}
	return content
}

func (h *harness) usageRows() []map[string]any {
	h.t.Helper()
	rows, err := h.d.Read.Query("SELECT model, input_tokens, output_tokens, cost_usd FROM usage ORDER BY id")
	if err != nil {
		h.t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []map[string]any
	for rows.Next() {
		var model string
		var in, outTok int64
		var cost sql.NullFloat64
		if err := rows.Scan(&model, &in, &outTok, &cost); err != nil {
			h.t.Fatal(err)
		}
		out = append(out, map[string]any{"model": model, "input": in, "output": outTok, "cost": cost.Float64})
	}
	return out
}

func TestForcedToolChoiceFollowsTheModelCatalog(t *testing.T) {
	for _, tc := range []struct {
		model string
		force string
		want  any
	}{
		{pricing.HaikuID, "any", map[string]any{"type": "any"}},
		{pricing.HaikuID, "probe", map[string]any{"type": "tool", "name": "probe"}},
		{pricing.SonnetID, "any", nil},
		{pricing.OpusID, "probe", nil},
	} {
		t.Run(tc.model+"/"+tc.force, func(t *testing.T) {
			h := newHarness(t,
				anthropictest.Reply("tool_use", anthropictest.ToolUse("c1", "probe", nil)),
				anthropictest.Reply("end_turn", anthropictest.Text("done")),
			)
			h.set("default_model", tc.model)
			view, _ := h.run("agent", "run", "--text", "go", "--tool-choice", tc.force)
			if view["state"] != "done" {
				t.Fatalf("view %v", view)
			}
			if len(h.bodies) != 2 {
				t.Fatalf("%d requests", len(h.bodies))
			}
			first, second := h.bodies[0], h.bodies[1]
			got, present := first["tool_choice"]
			if tc.want == nil && present || tc.want != nil && !jsonEqual(got, tc.want) {
				t.Errorf("first request tool_choice = %v (present %t), want %v", got, present, tc.want)
			}
			if _, ok := second["tool_choice"]; ok {
				t.Errorf("follow-up request kept tool_choice %v", second["tool_choice"])
			}
		})
	}
}

func jsonEqual(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}

func TestToolChoiceNamingAnUnavailableToolFailsTheRun(t *testing.T) {
	h := newHarness(t)
	view, _ := h.run("agent", "run", "--text", "go", "--tool-choice", "missing")
	if view["state"] != "failed" || view["error"] != `tool_choice names "missing", which is not available to this run` {
		t.Errorf("view %v", view)
	}
	if n := len(h.bodies); n != 0 {
		t.Errorf("%d requests sent", n)
	}
}

func TestRefusalCarriesItsCategoryIntoThreadAndRun(t *testing.T) {
	h := newHarness(t, anthropictest.Refusal("cyber", "Exploit development is not supported.", anthropictest.Text("partial")))
	view, _ := h.run("agent", "run", "--text", "go")
	if view["state"] != "failed" || view["error"] != "the model refused the request (stop reason: refusal, category: cyber)" {
		t.Errorf("view %v", view)
	}
	text := h.lastAssistantText(view["thread_id"])
	for _, want := range []string{"category: cyber", "Exploit development is not supported.", "Nothing it produced was kept"} {
		if !strings.Contains(text, want) {
			t.Errorf("thread message %s lacks %q", text, want)
		}
	}
	if strings.Contains(text, "partial") {
		t.Errorf("refused content was kept: %s", text)
	}
	if rows := h.usageRows(); len(rows) != 1 || rows[0]["model"] != pricing.HaikuID {
		t.Errorf("ledger %v", rows)
	}
}

func TestRefusalWithoutAFallbackNamesTheRecommendedModel(t *testing.T) {
	refused := anthropictest.Refusal("general_harms", "")
	refused.StopDetails = json.RawMessage(`{"type":"refusal","category":null,"explanation":null,"recommended_model":"claude-haiku-5-5"}`)
	h := newHarness(t, refused)
	h.set("default_model", pricing.OpusID)
	view, _ := h.run("agent", "run", "--text", "go")
	text := h.lastAssistantText(view["thread_id"])
	if view["error"] != "the model refused the request (stop reason: refusal)" || !strings.Contains(text, "retrying on claude-haiku-5-5") {
		t.Errorf("error %v, thread message %s", view["error"], text)
	}
}

func TestServerFallbackIsRecordedAndPricedByTheServingModel(t *testing.T) {
	usage := anthropictest.Usage{Input: 1000, Output: 100, Iterations: []anthropictest.Iteration{
		{Type: "message", Input: 600, Output: 10},
		{Type: "fallback_message", Model: pricing.HaikuID, Input: 400, Output: 90},
	}}
	h := newHarness(t, anthropictest.Reply("end_turn",
		anthropictest.Fallback(pricing.OpusID, pricing.HaikuID, "cyber"), anthropictest.Text("served by the fallback"),
	).WithUsage(usage))
	h.set("default_model", pricing.OpusID)
	view, out := h.run("agent", "run", "--text", "go")
	if view["state"] != "done" {
		t.Fatalf("view %s", out)
	}
	rows := h.usageRows()
	if len(rows) != 2 || rows[0]["model"] != pricing.OpusID || rows[1]["model"] != pricing.HaikuID {
		t.Fatalf("ledger %v", rows)
	}
	wantOpus, err := pricing.CostUSD(pricing.OpusID, pricing.Tokens{Input: 600, Output: 10}, 0)
	if err != nil {
		t.Fatal(err)
	}
	wantHaiku, err := pricing.CostUSD(pricing.HaikuID, pricing.Tokens{Input: 400, Output: 90}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if rows[0]["cost"] != wantOpus || rows[1]["cost"] != wantHaiku {
		t.Errorf("costs %v, want %v and %v", rows, wantOpus, wantHaiku)
	}
	if rows[1]["input"] != int64(400) || rows[1]["output"] != int64(90) {
		t.Errorf("fallback tokens %v", rows[1])
	}
	if cost := view["cost"].(float64); math.Abs(cost-(wantOpus+wantHaiku)) > 1e-12 {
		t.Errorf("run cost %v, want %v", cost, wantOpus+wantHaiku)
	}
}

func probeReplies(n int, vary bool) []anthropictest.Response {
	out := make([]anthropictest.Response, n)
	for i := range out {
		input := map[string]any{"n": 0}
		if vary {
			input["n"] = i
		}
		out[i] = anthropictest.Reply("tool_use", anthropictest.ToolUse("c"+string(rune('a'+i)), "probe", input))
	}
	return out
}

func TestStepCapIsPerRunKindAndEditable(t *testing.T) {
	h := newHarness(t)
	h.srv.Push(probeReplies(2, true)...)
	h.set("max_steps_cron", "2")
	view, _ := h.run("agent", "run", "--text", "go", "--kind", "cron")
	want := "stopped: step cap reached after 2 steps in this cron run (max_steps_cron)"
	if view["state"] != "failed" || view["error"] != want {
		t.Errorf("view %v", view)
	}
	h.srv.Push(probeReplies(3, true)...)
	h.set("max_steps_cron", "3")
	view, _ = h.run("agent", "run", "--text", "go", "--kind", "cron")
	if view["error"] != "stopped: step cap reached after 3 steps in this cron run (max_steps_cron)" {
		t.Errorf("view %v", view)
	}
	h.srv.Push(probeReplies(1, true)...)
	h.srv.Push(anthropictest.Reply("end_turn", anthropictest.Text("ok")))
	h.set("max_steps_chat", "5")
	view, _ = h.run("agent", "run", "--text", "go")
	if view["state"] != "done" {
		t.Errorf("a chat run is not bound by the cron cap: %v", view)
	}
	if n := h.srv.Remaining(); n != 0 {
		t.Errorf("%d scripted replies unused", n)
	}
}

func TestDoomLoopThresholdIsEditable(t *testing.T) {
	h := newHarness(t)
	h.srv.Push(probeReplies(3, false)...)
	view, _ := h.run("agent", "run", "--text", "go", "--kind", "cron")
	if view["state"] != "failed" || !strings.Contains(view["error"].(string), "repeated 3 times with the same result") {
		t.Errorf("view %v", view)
	}
	h.srv.Push(probeReplies(2, false)...)
	h.set("doom_loop_threshold", "2")
	view, _ = h.run("agent", "run", "--text", "go", "--kind", "cron")
	if !strings.Contains(view["error"].(string), "repeated 2 times with the same result") {
		t.Errorf("view %v", view)
	}
}

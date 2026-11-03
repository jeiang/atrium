package main

import (
	"path/filepath"
	"slices"
	"strconv"
	"testing"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/assistant/loop"
	"github.com/jeiang/atrium/internal/cli"
)

func toolNamesIn(t *testing.T, req anthropictest.Request) []string {
	t.Helper()
	var names []string
	list, _ := req.JSON(t)["tools"].([]any)
	for _, raw := range list {
		if entry, ok := raw.(map[string]any); ok {
			names = append(names, entry["name"].(string))
		}
	}
	return names
}

func wantWeb(t *testing.T, req anthropictest.Request, label string, want bool) {
	t.Helper()
	names := toolNamesIn(t, req)
	for _, name := range []string{"web_fetch", "web_search"} {
		if slices.Contains(names, name) != want {
			t.Errorf("%s: %s offered = %t, want %t", label, name, !want, want)
		}
	}
}

func taintFixture(t *testing.T) (string, *modelLog) {
	t.Helper()
	dir, api := moduleFixture(t, flagsExcept("", ""))
	if ran := runApp("agent", "run", "--text", "warm up", "--kind", "chat"); ran.code != cli.ExitOK {
		t.Fatalf("agent run: %d %s", ran.code, ran.stderr)
	}
	return dir, api
}

func chatTurn(t *testing.T, api *modelLog, thread int64, replies ...anthropictest.Response) []anthropictest.Request {
	t.Helper()
	before := len(api.Requests())
	api.Push(replies...)
	args := []string{"agent", "run", "--text", "please", "--kind", "chat"}
	if thread != 0 {
		args = append(args, "--thread", strconv.FormatInt(thread, 10))
	}
	if ran := runApp(args...); ran.code != cli.ExitOK {
		t.Fatalf("agent run: %d %s", ran.code, ran.stderr)
	}
	return api.Requests()[before:]
}

func toolResultText(t *testing.T, req anthropictest.Request, id string) (string, bool) {
	t.Helper()
	messages, _ := req.JSON(t)["messages"].([]any)
	for _, m := range messages {
		content, _ := m.(map[string]any)["content"].([]any)
		for _, raw := range content {
			block, _ := raw.(map[string]any)
			if block["type"] == "tool_result" && block["tool_use_id"] == id {
				text, _ := block["content"].(string)
				isError, _ := block["is_error"].(bool)
				return text, isError
			}
		}
	}
	t.Fatalf("no tool_result for %s", id)
	return "", false
}

func use(id, name string, input map[string]any) anthropictest.Response {
	return anthropictest.Reply("tool_use", anthropictest.ToolUse(id, name, input))
}

func done() anthropictest.Response { return anthropictest.Reply("end_turn", anthropictest.Text("ok")) }

func TestReadingPrivateDataStopsOutwardToolsForTheThread(t *testing.T) {
	dir, api := taintFixture(t)
	requests := chatTurn(t, api, 0, use("c0", "profile_get", nil), done())
	wantWeb(t, requests[0], "before the private read", true)
	wantWeb(t, requests[1], "after the private read in the same run", false)

	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
	thread := scalar[int64](t, d, "SELECT max(id) FROM threads")
	requests = chatTurn(t, api, thread, done())
	wantWeb(t, requests[0], "next run of the same thread", false)

	requests = chatTurn(t, api, 0, done())
	wantWeb(t, requests[0], "fresh thread", true)
}

func TestToolsThatOnlyReturnUntrustedTextDoNotTaint(t *testing.T) {
	_, api := taintFixture(t)
	requests := chatTurn(t, api, 0, use("c0", "grafana_datasources", nil), done())
	if !slices.Contains(toolNamesIn(t, requests[0]), "grafana_datasources") {
		t.Fatal("grafana_datasources is not offered, so this run proves nothing")
	}
	wantWeb(t, requests[1], "after an untrusted-output-only tool", true)
}

func TestWebSearchDoesNotRemoveTheWebTools(t *testing.T) {
	_, api := taintFixture(t)
	requests := chatTurn(t, api, 0, anthropictest.Reply("pause_turn", anthropictest.WebSearch("s0", "q", "https://example.com/a")...), done())
	wantWeb(t, requests[1], "after web_search", true)
}

func TestFetchInTheResponseAfterAPrivateReadIsRefused(t *testing.T) {
	dir, api := taintFixture(t)
	fetch := map[string]any{"url": "https://example.com/a"}
	requests := chatTurn(t, api, 0, anthropictest.Reply("tool_use",
		anthropictest.ToolUse("r", "profile_get", nil),
		anthropictest.ToolUse("f", "web_fetch", fetch),
	), done())
	text, isError := toolResultText(t, requests[1], "f")
	if !isError || text != loop.TaintRefusal {
		t.Errorf("fetch after the read = %q error=%v, want the taint refusal", text, isError)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
	if state := scalar[string](t, d, "SELECT state FROM tool_calls WHERE name = 'web_fetch'"); state != "error" {
		t.Errorf("refused fetch ledger state = %s", state)
	}

	requests = chatTurn(t, api, 0, anthropictest.Reply("tool_use",
		anthropictest.ToolUse("f", "web_fetch", fetch),
		anthropictest.ToolUse("r", "profile_get", nil),
	), done())
	if text, _ := toolResultText(t, requests[1], "f"); text == loop.TaintRefusal {
		t.Error("fetch ahead of the private read was refused")
	}
}

func TestResearchChildOfATaintedThreadStartsTainted(t *testing.T) {
	_, api := taintFixture(t)
	brief := map[string]any{"role": "web", "task": "look something up"}
	requests := chatTurn(t, api, 0, use("c0", "research", brief), done(), done())
	if !slices.Contains(toolNamesIn(t, requests[1]), "recall") {
		t.Fatalf("request 1 is not the web child: %v", toolNamesIn(t, requests[1]))
	}
	wantWeb(t, requests[1], "child of a clean thread", true)

	requests = chatTurn(t, api, 0, anthropictest.Reply("tool_use",
		anthropictest.ToolUse("r", "profile_get", nil),
		anthropictest.ToolUse("s", "research", brief),
	), done(), done())
	if !slices.Contains(toolNamesIn(t, requests[1]), "recall") {
		t.Fatalf("request 1 is not the web child: %v", toolNamesIn(t, requests[1]))
	}
	wantWeb(t, requests[1], "child of a tainted thread", false)
}

func TestDisablingAModuleKeepsTheThreadTainted(t *testing.T) {
	dir, api := taintFixture(t)
	requests := chatTurn(t, api, 0, use("c0", "mail_search", map[string]any{"query": "x"}), done())
	if !slices.Contains(toolNamesIn(t, requests[0]), "mail_search") {
		t.Fatal("mail_search is not offered, so this run proves nothing")
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
	thread := scalar[int64](t, d, "SELECT max(id) FROM threads")

	t.Setenv(flagEnv("mail"), "false")
	requests = chatTurn(t, api, thread, done())
	if slices.Contains(toolNamesIn(t, requests[0]), "mail_search") {
		t.Fatal("mail module is still on")
	}
	wantWeb(t, requests[0], "thread whose mail module was turned off", false)
}

func TestResearchChildPrivateReadTaintsTheParent(t *testing.T) {
	_, api := taintFixture(t)
	brief := map[string]any{"role": "web", "task": "look something up"}
	requests := chatTurn(t, api, 0, use("c0", "research", brief), done(), done())
	wantWeb(t, requests[2], "parent after a research child that read nothing private", true)

	fetch := map[string]any{"url": "https://example.com/a"}
	requests = chatTurn(t, api, 0, anthropictest.Reply("tool_use",
		anthropictest.ToolUse("s", "research", brief),
		anthropictest.ToolUse("f", "web_fetch", fetch),
	), use("p", "profile_get", nil), done(), done())
	var parent anthropictest.Request
	for _, r := range requests {
		if !slices.Contains(toolNamesIn(t, r), "research") {
			continue
		}
		parent = r
	}
	wantWeb(t, parent, "parent after a research child that read private data", false)
	text, isError := toolResultText(t, parent, "f")
	if !isError || text != loop.TaintRefusal {
		t.Errorf("fetch after the research call = %q error=%v, want the taint refusal", text, isError)
	}
}

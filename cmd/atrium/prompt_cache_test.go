package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/assistant/memory"
	"github.com/jeiang/atrium/internal/cli"
)

func withoutCacheControl(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			if k != "cache_control" {
				out[k] = withoutCacheControl(val)
			}
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			out[i] = withoutCacheControl(val)
		}
		return out
	}
	return v
}

func cacheMarks(body map[string]any) []string {
	var marks []string
	mark := func(path string, v any) {
		if b, ok := v.(map[string]any); ok && b["cache_control"] != nil {
			marks = append(marks, path)
		}
	}
	for i, v := range body["tools"].([]any) {
		mark(fmt.Sprintf("tools/%d", i), v)
	}
	for i, v := range body["system"].([]any) {
		mark(fmt.Sprintf("system/%d", i), v)
	}
	for i, m := range body["messages"].([]any) {
		for j, v := range m.(map[string]any)["content"].([]any) {
			mark(fmt.Sprintf("messages/%d/%d", i, j), v)
		}
	}
	return marks
}

func contentOf(body map[string]any, i int) []any {
	return body["messages"].([]any)[i].(map[string]any)["content"].([]any)
}

func cachedPrefix(t *testing.T, body map[string]any, through int) string {
	t.Helper()
	raw, err := json.Marshal(withoutCacheControl(map[string]any{
		"tools": body["tools"], "system": body["system"], "head": contentOf(body, 0)[:through+1],
	}))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestChatStepsReadTheCachedPrefixAndSeeTheTailVerbatim(t *testing.T) {
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	if ran := runApp(t, "agent", "run", "--text", "warm the thread"); ran.code != cli.ExitOK {
		t.Fatalf("warm run: %d %s", ran.code, ran.stderr)
	}
	thread := latestThread(t, dir)
	seedSettings(t, map[string]string{memory.VerbatimTokensKey: "0"})

	long := strings.TrimSpace(strings.Repeat("The operator explains one more constraint in detail. ", 60))
	items := make([]map[string]any, 20)
	for i := range items {
		items[i] = map[string]any{"text": fmt.Sprintf("Step %02d: %s", i, strings.TrimSpace(strings.Repeat("check the long result survives ", 4)))}
	}
	api.Push(
		anthropictest.Reply("tool_use", anthropictest.ToolUse("p1", "plan", map[string]any{"items": items})),
		anthropictest.Reply("tool_use", anthropictest.ToolUse("p2", "plan", map[string]any{"items": items[:2]})),
		anthropictest.Reply("end_turn", anthropictest.Text("done")),
	)
	loc, err := time.LoadLocation("America/Port_of_Spain")
	if err != nil {
		t.Fatal(err)
	}
	startedAt := time.Now().Add(-time.Minute)
	sent := len(api.Requests())
	if ran := runApp(t, "agent", "run", "--thread", fmt.Sprint(thread), "--text", long); ran.code != cli.ExitOK {
		t.Fatalf("agent run: %d %s", ran.code, ran.stderr)
	}
	endedAt := time.Now().Add(time.Minute)
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
	defer func() { _ = d.Close() }()
	planned := "ok thread:" + fmt.Sprint(thread) + " "
	if got := operationAudits(t, d, "assistant.plan.replace"); len(got) != 2 || !strings.HasPrefix(got[0], planned) || !strings.HasPrefix(got[1], planned) {
		t.Errorf("plan audits: %q", got)
	}
	requests := api.Requests()[sent:]
	if len(requests) != 3 {
		t.Fatalf("%d model requests, want 3", len(requests))
	}

	var first string
	var previous []any
	for n, r := range requests {
		body := r.JSON(t)
		tools, system, messages := body["tools"].([]any), body["system"].([]any), body["messages"].([]any)
		head := contentOf(body, 0)
		view := -1
		for j, v := range head {
			if text, _ := v.(map[string]any)["text"].(string); text == long {
				view = j - 1
			}
		}
		if view < 1 || !strings.Contains(fmt.Sprint(head[:view+1]), "warm the thread") {
			t.Fatalf("request %d: the operator message does not follow the older memory view: %v", n, head)
		}
		last := messages[len(messages)-1].(map[string]any)["content"].([]any)
		want := []string{
			fmt.Sprintf("tools/%d", len(tools)-1),
			fmt.Sprintf("system/%d", len(system)-1),
			fmt.Sprintf("messages/0/%d", view),
			fmt.Sprintf("messages/%d/%d", len(messages)-1, len(last)-1),
		}
		if got := cacheMarks(body); !reflect.DeepEqual(got, want) {
			t.Errorf("request %d: cache breakpoints %v, want %v", n, got, want)
		}
		prefix := cachedPrefix(t, body, view)
		if n == 0 {
			first = prefix
		} else if prefix != first {
			t.Errorf("request %d: the cached prefix differs from the first request's", n)
		}
		if n > 0 && !reflect.DeepEqual(withoutCacheControl(messages[:len(previous)]), withoutCacheControl(previous)) {
			t.Errorf("request %d does not extend request %d", n, n-1)
		}
		previous = messages
	}

	var result string
	for _, m := range requests[1].JSON(t)["messages"].([]any) {
		for _, v := range m.(map[string]any)["content"].([]any) {
			if b := v.(map[string]any); b["type"] == "tool_result" && b["tool_use_id"] == "p1" {
				result = fmt.Sprint(b["content"])
			}
		}
	}
	if len(result) < 2000 {
		t.Fatalf("the plan result reached the model clipped: %q", result)
	}
	for _, it := range items {
		if !strings.Contains(result, it["text"].(string)) {
			t.Errorf("the plan result lacks %q", it["text"])
		}
	}

	const stamp = "Current date and time: "
	var stamped bool
	for _, v := range contentOf(requests[0].JSON(t), 0) {
		text, _ := v.(map[string]any)["text"].(string)
		if !strings.HasPrefix(text, stamp) {
			continue
		}
		at, err := time.ParseInLocation("Monday 2006-01-02 15:04 MST", strings.TrimPrefix(text, stamp), loc)
		if err != nil {
			t.Fatalf("time stamp %q: %v", text, err)
		}
		if _, offset := at.Zone(); offset != -4*3600 || at.Before(startedAt) || at.After(endedAt) {
			t.Errorf("time stamp %q is not the run start in America/Port_of_Spain", text)
		}
		stamped = true
	}
	if !stamped {
		t.Error("the operator message carries no current date and time")
	}
}

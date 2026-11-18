package main

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
)

func sendNowTurn(t *testing.T, api *modelLog, from int, text string) []any {
	t.Helper()
	for _, r := range api.Requests()[from:] {
		messages, _ := r.JSON(t)["messages"].([]any)
		if len(messages) == 0 {
			continue
		}
		last, _ := messages[len(messages)-1].(map[string]any)
		content, _ := last["content"].([]any)
		for _, raw := range content {
			if b, _ := raw.(map[string]any); b["type"] == "text" && b["text"] == text {
				return content
			}
		}
	}
	t.Fatalf("no model request ended with the operator's %q", text)
	return nil
}

func sendNowResult(t *testing.T, content []any, toolUseID string) (map[string]any, int) {
	t.Helper()
	for i, raw := range content {
		if b, _ := raw.(map[string]any); b["type"] == "tool_result" && b["tool_use_id"] == toolUseID {
			return b, i
		}
	}
	t.Fatalf("no tool result for %s in %v", toolUseID, content)
	return nil, 0
}

func sendNowTextAt(content []any, text string) int {
	for i, raw := range content {
		if b, _ := raw.(map[string]any); b["type"] == "text" && b["text"] == text {
			return i
		}
	}
	return -1
}

func TestAsstQueuedMessageUnsendsUntilARunTakesIt(t *testing.T) {
	t.Parallel()
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	asstWarm(t)
	d := asstDB(t, dir)
	base, _, _ := startServeStoppable(t, dir)
	w := localWebG1(t, base)

	api.Push(asstReply("first"), asstReply("second"))
	release := asstHold(t, api)
	sent := len(api.Requests())
	thread := webStartG1(t, w, map[string]any{"text": "start"})
	asstWaitRequests(t, api, sent+1)

	withdrawn := w.do(http.MethodPost, threadG1(thread, "/messages"), map[string]any{"text": "never mind this"})
	webWantG1(t, withdrawn, http.StatusAccepted)
	withdrawnID := number(t, withdrawn.json["queued_id"])
	unsent := w.do(http.MethodDelete, threadG1(thread, "/queued/"+strconv.FormatInt(withdrawnID, 10)), nil)
	webWantG1(t, unsent, http.StatusOK)
	if unsent.json["text"] != "never mind this" || number(t, unsent.json["id"]) != withdrawnID {
		t.Fatalf("unsend returned %s, want the withdrawn text", unsent.body)
	}
	expectAPI(t, w.do(http.MethodDelete, threadG1(thread, "/queued/"+strconv.FormatInt(withdrawnID, 10)), nil), http.StatusNotFound, "not_found")
	if queued, _ := webOKG1(t, w.do(http.MethodGet, threadG1(thread, ""), nil))["queued"].([]any); len(queued) != 0 {
		t.Fatalf("queued after unsend: %v", queued)
	}

	kept := w.do(http.MethodPost, threadG1(thread, "/messages"), map[string]any{"text": "keep this one"})
	webWantG1(t, kept, http.StatusAccepted)
	keptID := number(t, kept.json["queued_id"])
	run := asstLastRun(t, d, thread)
	release()
	if state, errText := asstSettle(t, d, run); state != "done" {
		t.Fatalf("run %s %q", state, errText)
	}
	expectAPI(t, w.do(http.MethodDelete, threadG1(thread, "/queued/"+strconv.FormatInt(keptID, 10)), nil), http.StatusConflict, "conflict")
	took := false
	for _, r := range api.Requests()[sent:] {
		if strings.Contains(string(r.Body), "never mind this") {
			t.Fatal("the unsent message reached the model")
		}
		took = took || strings.Contains(string(r.Body), "keep this one")
	}
	if !took {
		t.Fatal("the kept message never reached the model")
	}
}

func TestAsstSendNowInterruptsAReadOnlyToolAndContinuesTheRun(t *testing.T) {
	t.Parallel()
	dir, fake, api, d := kbEmbedFixture(t)
	asstWarm(t)
	entered := make(chan struct{}, 1)
	fake.setGate(func(ctx context.Context, texts []string) {
		if len(texts) == 1 && strings.Contains(texts[0], "slowquery") {
			select {
			case entered <- struct{}{}:
			default:
			}
			<-ctx.Done()
		}
	})
	base, _, _ := startServeStoppable(t, dir)
	w := localWebG1(t, base)

	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("search-1", "kb_search", map[string]any{"query": "slowquery"})), asstReply("answered the operator"))
	sent := len(api.Requests())
	thread := webStartG1(t, w, map[string]any{"text": "look it up"})
	select {
	case <-entered:
	case <-time.After(20 * time.Second):
		t.Fatal("kb_search never reached the embedder")
	}
	now := w.do(http.MethodPost, threadG1(thread, "/messages"), map[string]any{"text": "skip the search, answer now", "now": true})
	webWantG1(t, now, http.StatusAccepted)
	if now.json["queued_id"] == nil {
		t.Fatalf("send now during a run: %s", now.body)
	}
	run := asstLastRun(t, d, thread)
	if state, errText := asstSettle(t, d, run); state != "done" {
		t.Fatalf("run %s %q", state, errText)
	}
	content := sendNowTurn(t, api, sent, "skip the search, answer now")
	result, at := sendNowResult(t, content, "search-1")
	text, _ := result["content"].(string)
	if result["is_error"] != true || !strings.Contains(text, "interrupted by the operator") || !strings.Contains(text, "search-1") || !strings.Contains(text, "kb_search") {
		t.Fatalf("interrupted result %v", result)
	}
	if after := sendNowTextAt(content, "skip the search, answer now"); after < at {
		t.Fatalf("the operator text came before the tool result: %v", content)
	}
	if n := asstCount(t, d, "SELECT COUNT(*) FROM tool_calls WHERE run_id = ? AND tool_use_id = 'search-1' AND state = 'error' AND error LIKE '%interrupted by the operator%'", run); n != 1 {
		t.Fatalf("interrupted tool call rows %d, want 1", n)
	}
	if n := asstCount(t, d, "SELECT COUNT(*) FROM thread_queue WHERE thread_id = ? AND consumed_at IS NOT NULL AND run_id = ?", thread, run); n != 1 {
		t.Fatalf("taken queue rows %d, want 1", n)
	}
	if n := asstCount(t, d, "SELECT COUNT(*) FROM runs WHERE thread_id = ?", thread); n != 1 {
		t.Fatalf("runs %d, want the same run to continue", n)
	}
}

func TestAsstSendNowLetsARunningWriteFinish(t *testing.T) {
	t.Parallel()
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	push, browser := notifyPushRig(t, dir)
	if ran := runApp(t, "agent", "run", "--text", "warm"); ran.code != cli.ExitOK {
		t.Fatalf("warm run: %d %s", ran.code, ran.stderr)
	}
	d := asstDB(t, dir)
	browser.store(t, d)
	free := push.holdUntil()
	base, _, _ := startServeStoppable(t, dir)
	w := localWebG1(t, base)

	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("note-1", "notify", map[string]any{"title": "Heads up", "body": "slow push"})), asstReply("noted"))
	sent := len(api.Requests())
	thread := webStartG1(t, w, map[string]any{"text": "tell my phone"})
	asstWaitCount(t, d, 1, "SELECT COUNT(*) FROM tool_calls WHERE tool_use_id = 'note-1' AND state = 'started'")
	webWantG1(t, w.do(http.MethodPost, threadG1(thread, "/messages"), map[string]any{"text": "also say hi", "now": true}), http.StatusAccepted)
	time.Sleep(2 * time.Second)
	run := asstLastRun(t, d, thread)
	if n := asstCount(t, d, "SELECT COUNT(*) FROM tool_calls WHERE run_id = ? AND tool_use_id = 'note-1' AND state = 'started'", run); n != 1 {
		t.Fatalf("the write was cut off by send now: %d started rows", n)
	}
	free()
	if state, errText := asstSettle(t, d, run); state != "done" {
		t.Fatalf("run %s %q", state, errText)
	}
	content := sendNowTurn(t, api, sent, "also say hi")
	result, at := sendNowResult(t, content, "note-1")
	text, _ := result["content"].(string)
	if result["is_error"] == true || strings.Contains(text, "interrupted") || !strings.Contains(text, "recorded") {
		t.Fatalf("write result %v, want its real result", result)
	}
	if after := sendNowTextAt(content, "also say hi"); after < at {
		t.Fatalf("the operator text came before the tool result: %v", content)
	}
	if n := asstCount(t, d, "SELECT COUNT(*) FROM tool_calls WHERE run_id = ? AND tool_use_id = 'note-1' AND state = 'done'", run); n != 1 {
		t.Fatalf("finished write rows %d, want 1", n)
	}
}

func TestAsstSendNowAnswersAParkedPermissionRequestWithAMessage(t *testing.T) {
	t.Parallel()
	srv, pages := webPages(t)
	dir, api, f := moduleFixture(t, flagsExcept("", ""))
	warm(t, api)
	thread := latestThread(t, dir)
	base, _, _ := startServeApp(t, freshApp(f), &lockedBuffer{})
	client := webLocalG2(t, base)
	start := len(api.Requests())
	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("f1", "web_fetch", map[string]any{"url": srv.URL + "/a"})))
	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("f2", "web_fetch", map[string]any{"url": srv.URL + "/b"})))
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("summarized a")))
	webOKG2(t, client.do(http.MethodPost, fmt.Sprintf("/web/v1/threads/%d/messages", thread), map[string]any{"text": "read " + srv.URL + "/a then " + srv.URL + "/b"}))
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), db.Key{})
	defer func() { _ = d.Close() }()
	proposal := waitParkedPermission(t, d, thread)
	var run int64
	if err := d.Read.QueryRow("SELECT id FROM runs WHERE thread_id = ? ORDER BY id DESC LIMIT 1", thread).Scan(&run); err != nil {
		t.Fatal(err)
	}
	took := client.do(http.MethodPost, fmt.Sprintf("/web/v1/threads/%d/messages", thread), map[string]any{"text": "skip b, summarize a", "now": true})
	webWantG1(t, took, http.StatusAccepted)
	if took.json["run_id"] == nil || number(t, took.json["run_id"]) != run {
		t.Fatalf("send now on a parked run: %s, want run %d", took.body, run)
	}
	waitRunState(t, d, thread, "done")
	content := sendNowTurn(t, api, start, "skip b, summarize a")
	result, at := sendNowResult(t, content, "f2")
	text, _ := result["content"].(string)
	if result["is_error"] != true || !strings.Contains(text, "interrupted by the operator") || !strings.Contains(text, "f2") {
		t.Fatalf("parked call result %v", result)
	}
	if after := sendNowTextAt(content, "skip b, summarize a"); after < at {
		t.Fatalf("the operator text came before the tool result: %v", content)
	}
	var status string
	if err := d.Read.QueryRow("SELECT status FROM proposals WHERE id = ?", proposal).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "dismissed" {
		t.Fatalf("permission request %s, want dismissed", status)
	}
	if hits := pages.seen(); len(hits) != 1 || hits[0] != "/a" {
		t.Fatalf("page fetches = %v, want /a only", hits)
	}
	if n := asstCount(t, d, "SELECT COUNT(*) FROM runs WHERE thread_id = ? AND id > ?", thread, run); n != 0 {
		t.Fatalf("%d new runs, want the parked run to continue", n)
	}
}

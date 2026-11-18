package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/anthropic"
	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/assistant/loop"
	"github.com/jeiang/atrium/internal/assistant/pricing"
)

func loopRequests(t *testing.T, requests []anthropictest.Request) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, r := range requests {
		if !strings.HasSuffix(r.Path, "/v1/messages") {
			continue
		}
		body := r.JSON(t)
		if body["tools"] == nil {
			continue
		}
		body["_betas"] = strings.Join(r.Header.Values("Anthropic-Beta"), ",")
		out = append(out, body)
	}
	return out
}

func startThread(t *testing.T, s *apiSite, token string, body map[string]any) int64 {
	t.Helper()
	started := s.do(http.MethodPost, "/api/v1/threads", token, body)
	if started.status != http.StatusCreated || started.json["run_id"] == nil {
		t.Fatalf("start: %d %s", started.status, started.body)
	}
	thread, _ := started.json["thread"].(map[string]any)
	return number(t, thread["id"])
}

func threadLimit(t *testing.T, view apiReply) int64 {
	t.Helper()
	thread, _ := view.json["thread"].(map[string]any)
	return number(t, thread["context_limit"])
}

func editTriggers(t *testing.T, body map[string]any) [4]int64 {
	t.Helper()
	cm, _ := body["context_management"].(map[string]any)
	edits, _ := cm["edits"].([]any)
	if len(edits) != 2 {
		t.Fatalf("context_management edits: %v", body["context_management"])
	}
	value := func(m map[string]any, key string) int64 {
		inner, _ := m[key].(map[string]any)
		return number(t, inner["value"])
	}
	clear, _ := edits[0].(map[string]any)
	compact, _ := edits[1].(map[string]any)
	if clear["type"] != anthropic.EditClearToolUses || compact["type"] != anthropic.EditCompact {
		t.Fatalf("edit types: %v", edits)
	}
	return [4]int64{value(clear, "trigger"), value(clear, "keep"), value(clear, "clear_at_least"), value(compact, "trigger")}
}

func carriesBlock(t *testing.T, body map[string]any, want json.RawMessage) bool {
	t.Helper()
	var expected any
	if err := json.Unmarshal(want, &expected); err != nil {
		t.Fatal(err)
	}
	messages, _ := body["messages"].([]any)
	for _, raw := range messages {
		m, _ := raw.(map[string]any)
		content, _ := m["content"].([]any)
		for _, b := range content {
			if m["role"] == "assistant" && reflect.DeepEqual(b, expected) {
				return true
			}
		}
	}
	return false
}

func TestRunStopLeavesATranscriptItemAndContinueStartsANewRun(t *testing.T) {
	t.Parallel()
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	warm(t, api)
	s := startAPISite(t, dir, api)
	owner := s.token("owner")

	api.Push(asstProbes("s", loop.TuningMaxStepsChat, true)...)
	id := startThread(t, s, owner, map[string]any{"text": "sort the receipts", "web_search": false})
	view := waitThreadSettled(t, s, owner, id)
	want := fmt.Sprintf("The run stopped after %d steps, the most one chat run may take.", loop.TuningMaxStepsChat)
	var stop map[string]any
	for _, item := range transcriptItems(view) {
		if item["kind"] == "stop" {
			stop = item
		}
	}
	if stop == nil || stop["text"] != want || stop["run_id"] == nil {
		t.Fatalf("no stop item %q in the transcript: %s", want, view.body)
	}
	run := s.do(http.MethodGet, threadPath(id, "/runs/"+strconv.FormatInt(number(t, stop["run_id"]), 10)), owner, nil)
	if run.status != http.StatusOK || run.json["state"] != "failed" || !strings.Contains(fmt.Sprint(run.json["error"]), "step cap reached") {
		t.Fatalf("stopped run: %d %s", run.status, run.body)
	}

	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("picked up where it stopped")))
	before := len(api.Requests())
	sent := s.do(http.MethodPost, threadPath(id, "/messages"), owner, map[string]any{"text": "Continue."})
	if sent.status != http.StatusAccepted || sent.json["run_id"] == nil || number(t, sent.json["run_id"]) == number(t, stop["run_id"]) {
		t.Fatalf("continue: %d %s", sent.status, sent.body)
	}
	view = waitThreadSettled(t, s, owner, id)
	items := transcriptItems(view)
	if tail := items[len(items)-1]; tail["kind"] != "assistant" || tail["text"] != "picked up where it stopped" {
		t.Fatalf("after continue: %s", view.body)
	}
	kept := false
	for _, item := range items {
		kept = kept || (item["kind"] == "stop" && item["text"] == want)
	}
	if !kept {
		t.Fatalf("the stop item did not stay in the transcript: %s", view.body)
	}
	requests := loopRequests(t, api.Requests()[before:])
	if len(requests) != 1 {
		t.Fatalf("%d model requests for the continued run, want 1", len(requests))
	}
	raw, err := json.Marshal(requests[0]["messages"])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "atrium note: "+want) || !strings.Contains(string(raw), "Continue.") {
		t.Fatalf("continued request misses the stop note: %s", raw)
	}
}

func TestChatRequestsCarryContextEditsAndReplayCompaction(t *testing.T) {
	t.Parallel()
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	warm(t, api)
	s := startAPISite(t, dir, api)
	owner := s.token("owner")

	compaction := anthropictest.Compaction("Summary of the earlier turns.", "opaque-compaction-1")
	api.Push(
		anthropictest.Reply("tool_use", compaction, anthropictest.ToolUse("c1", "profile_get", map[string]any{"key": "k"})),
		anthropictest.Reply("end_turn", anthropictest.Text("first done")),
	)
	before := len(api.Requests())
	haiku := startThread(t, s, owner, map[string]any{"text": "look it up", "web_search": false})
	view := waitThreadSettled(t, s, owner, haiku)
	if got := threadLimit(t, view); got != 200_000 {
		t.Fatalf("new Haiku thread limit %d, want the model default 200000", got)
	}
	requests := loopRequests(t, api.Requests()[before:])
	if len(requests) != 2 {
		t.Fatalf("%d model requests, want 2", len(requests))
	}
	for i, body := range requests {
		if got := editTriggers(t, body); got != [4]int64{100_000, 5, 20_000, 200_000} {
			t.Errorf("request %d triggers %v, want clear at 100000 keeping 5 (at least 20000), compact at 200000", i, got)
		}
		if betas := body["_betas"].(string); !strings.Contains(betas, anthropic.BetaContextManagement) || !strings.Contains(betas, anthropic.BetaCompaction) {
			t.Errorf("request %d betas %q", i, betas)
		}
	}
	if !carriesBlock(t, requests[1], compaction) {
		t.Fatalf("the next step does not replay the compaction block verbatim: %v", requests[1]["messages"])
	}

	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("second done")))
	before = len(api.Requests())
	if sent := s.do(http.MethodPost, threadPath(haiku, "/messages"), owner, map[string]any{"text": "and again"}); sent.status != http.StatusAccepted {
		t.Fatalf("send: %d %s", sent.status, sent.body)
	}
	waitThreadSettled(t, s, owner, haiku)
	requests = loopRequests(t, api.Requests()[before:])
	if len(requests) != 1 || !carriesBlock(t, requests[0], compaction) {
		t.Fatalf("the next run does not replay the compaction block: %d requests", len(requests))
	}

	if changed := s.do(http.MethodPatch, threadPath(haiku, "/options"), owner, map[string]any{"model": pricing.SonnetID}); changed.status != http.StatusOK {
		t.Fatalf("switch model: %d %s", changed.status, changed.body)
	}
	if got := threadLimit(t, s.do(http.MethodGet, threadPath(haiku, ""), owner, nil)); got != 200_000 {
		t.Fatalf("switching the model changed the thread limit to %d", got)
	}

	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("sonnet done")))
	before = len(api.Requests())
	sonnet := startThread(t, s, owner, map[string]any{"text": "hello", "model": pricing.SonnetID, "web_search": false})
	view = waitThreadSettled(t, s, owner, sonnet)
	if got := threadLimit(t, view); got != 500_000 {
		t.Fatalf("new Sonnet thread limit %d, want the model default 500000", got)
	}
	requests = loopRequests(t, api.Requests()[before:])
	if len(requests) != 1 {
		t.Fatalf("%d Sonnet requests, want 1", len(requests))
	}
	if got := editTriggers(t, requests[0]); got != [4]int64{300_000, 5, 50_000, 400_000} {
		t.Errorf("Sonnet triggers %v, want clear at 300000 keeping 5 (at least 50000), compact at 400000", got)
	}
}

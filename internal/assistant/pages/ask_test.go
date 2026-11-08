package pages

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/assistant/journal"
)

func TestAskOperatorParksAndResumes(t *testing.T) {
	s := newSite(t)
	s.api.Push(reply1(anthropictest.ToolUse("ask-1", "ask_operator", map[string]any{"question": "Which account?"})))
	s.api.Push(reply1(anthropictest.Text("done")))
	thread := s.startThread("check my accounts")
	run := s.settle(s.lastRun(thread))
	if run.State != journal.Parked {
		t.Fatalf("state %q, want parked", run.State)
	}
	if run.Error != nil {
		t.Fatalf("parked run has error %q", *run.Error)
	}
	page := status(t, s.get(fmt.Sprintf("/chat/%d", thread)), http.StatusOK)
	mustContain(t, page, "Which account?")
	status(t, s.post(fmt.Sprintf("/chat/%d/answer", thread), url.Values{"text": {"personal"}}, hx, "true"), http.StatusOK)
	run = s.settle(s.lastRun(thread))
	if run.State != journal.Done {
		t.Fatalf("state %q, want done", run.State)
	}
	requests := s.api.Requests()
	if len(requests) < 2 {
		t.Fatalf("%d model requests, want 2", len(requests))
	}
	raw, err := json.Marshal(requests[len(requests)-1].JSON(t)["messages"])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "personal") {
		t.Errorf("resumed request misses the answer: %s", raw)
	}
	if !strings.Contains(string(raw), "tool_result") {
		t.Errorf("resumed request misses the tool result: %s", raw)
	}
}

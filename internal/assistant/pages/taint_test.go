package pages

import (
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"testing"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
)

func TestPrivateDataTaintsLaterRuns(t *testing.T) {
	s := newSite(t)
	s.api.Push(reply1(anthropictest.ToolUse("call-1", "profile_get", nil)))
	s.api.Push(reply1(anthropictest.Text("done")))
	thread := s.startThread("read my profile")
	s.settle(s.lastRun(thread))
	s.api.Push(reply1(anthropictest.Text("again")))
	status(t, s.post(fmt.Sprintf("/chat/%d/messages", thread), url.Values{"text": {"and again"}}, hx, "true"), http.StatusOK)
	s.settle(s.lastRun(thread))
	requests := s.api.Requests()
	if len(requests) < 3 {
		t.Fatalf("%d model requests, want at least 3", len(requests))
	}
	body := requests[len(requests)-1].JSON(t)
	tools, _ := body["tools"].([]any)
	var names []string
	for _, raw := range tools {
		if entry, ok := raw.(map[string]any); ok {
			if name, ok := entry["name"].(string); ok {
				names = append(names, name)
			}
		}
	}
	for _, name := range []string{"notify", "web_fetch", "web_search", "file_write", "profile_set"} {
		if slices.Contains(names, name) {
			t.Errorf("%s was offered after a private-data read: %v", name, names)
		}
	}
	if !slices.Contains(names, "profile_get") {
		t.Errorf("profile_get was dropped by taint: %v", names)
	}
}

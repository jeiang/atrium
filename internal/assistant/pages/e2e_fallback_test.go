package pages

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/assistant/journal"
	"github.com/jeiang/atrium/internal/assistant/pricing"
)

func TestChatPageShowsAServerFallback(t *testing.T) {
	s := newSite(t)
	usage := anthropictest.Usage{Input: 1000, Output: 100, Iterations: []anthropictest.Iteration{
		{Type: "message", Input: 600, Output: 10},
		{Type: "fallback_message", Model: pricing.OpusID, Input: 400, Output: 90},
	}}
	s.api.Push(reply1(
		anthropictest.Fallback(pricing.HaikuID, pricing.OpusID, "cyber"), anthropictest.Text("served by the fallback"),
	).WithUsage(usage))
	thread := s.startThread("hi")
	if got := s.settle(s.lastRun(thread)); got.State != journal.Done {
		t.Fatalf("run %s %v", got.State, got.Error)
	}
	body := status(t, s.get(fmt.Sprintf("/chat/%d", thread)), http.StatusOK)
	mustContain(t, body, pricing.HaikuID+" declined; "+pricing.OpusID+" took over (refusal category: cyber).", "served by the fallback")
	if n := s.count("SELECT COUNT(*) FROM usage"); n != 1 {
		t.Errorf("%d usage rows, want one per request", n)
	}
	if n := s.count("SELECT COUNT(*) FROM usage WHERE model = ? AND iterations LIKE ?", pricing.HaikuID, "%"+pricing.OpusID+"%"); n != 1 {
		t.Errorf("%d rows carry the fallback iteration", n)
	}
}

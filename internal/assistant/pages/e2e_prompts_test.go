package pages

import (
	"testing"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
)

func (s *site) requestOfRun(text string) string {
	s.t.Helper()
	before := len(s.api.Requests())
	s.api.Push(reply1(anthropictest.Text("ok")))
	s.settle(s.lastRun(s.startThread(text)))
	req := s.api.Requests()[before]
	if system, _ := req.JSON(s.t)["system"].([]any); len(system) == 0 {
		s.t.Fatalf("request without system blocks: %s", req.Body)
	}
	return string(req.Body)
}

func TestPromptCarriesNoSkillsIndex(t *testing.T) {
	s := newSite(t)
	mustNotContain(t, s.requestOfRun("hello"), "load_skill", "Skills (operator-written")
}

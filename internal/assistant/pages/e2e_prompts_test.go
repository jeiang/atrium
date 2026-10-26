package pages

import (
	"html/template"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/assistant/prompts"
)

func (s *site) systemOfRun(text string) (string, string) {
	s.t.Helper()
	before := len(s.api.Requests())
	s.api.Push(reply1(anthropictest.Text("ok")))
	s.settle(s.lastRun(s.startThread(text)))
	req := s.api.Requests()[before]
	system, _ := req.JSON(s.t)["system"].([]any)
	if len(system) == 0 {
		s.t.Fatalf("request without system blocks: %s", req.Body)
	}
	first, _ := system[0].(map[string]any)
	head, _ := first["text"].(string)
	return head, string(req.Body)
}

func TestPromptOverrideKeepsThePreambleAndResetRestoresTheDefault(t *testing.T) {
	s := newSite(t)
	page := status(t, s.get("/settings/prompts"), http.StatusOK)
	mustContain(t, page, template.HTMLEscapeString(prompts.Preamble), ">"+template.HTMLEscapeString(prompts.System)+"</textarea>", `action="/settings/prompts/prompt_summary"`)
	mustNotContain(t, page, "Reset to default")

	saved := s.post("/settings/prompts/prompt_system", url.Values{"text": {"Answer in French.\r\nBe brief."}})
	if saved.StatusCode != http.StatusSeeOther || saved.Header.Get("Location") != "/settings/prompts?saved=prompt_system" {
		t.Fatalf("save: %d %q", saved.StatusCode, saved.Header.Get("Location"))
	}
	status(t, s.post("/settings/prompts/prompt_summary", url.Values{"text": {"Keep only decisions."}}), http.StatusSeeOther)
	mustContain(t, status(t, s.get("/settings/prompts?saved=prompt_system"), http.StatusOK), "Prompt saved", "custom", "Reset to default", ">Answer in French.\nBe brief.</textarea>")
	if n := s.count("SELECT COUNT(*) FROM settings_audit WHERE key = 'prompt_system' AND source = 'ui'"); n != 1 {
		t.Errorf("%d audit rows for the prompt edit", n)
	}

	head, body := s.systemOfRun("hello")
	if head != prompts.Preamble+"\n\nAnswer in French.\nBe brief." {
		t.Errorf("custom system block %q", head)
	}
	mustContain(t, body, `"instructions":"Keep only decisions."`)

	blank := status(t, s.post("/settings/prompts/prompt_system", url.Values{"text": {"  "}}), http.StatusUnprocessableEntity)
	mustContain(t, blank, "must not be blank", `role="alert"`)
	status(t, s.post("/settings/prompts/prompt_preamble", url.Values{"text": {"gone"}}), http.StatusNotFound)

	reset := s.post("/settings/prompts/prompt_system/reset", nil)
	if reset.StatusCode != http.StatusSeeOther || reset.Header.Get("Location") != "/settings/prompts?reset=prompt_system" {
		t.Fatalf("reset: %d %q", reset.StatusCode, reset.Header.Get("Location"))
	}
	status(t, s.post("/settings/prompts/prompt_summary/reset", nil), http.StatusSeeOther)
	if n := s.count("SELECT COUNT(*) FROM settings WHERE key LIKE 'prompt_%'"); n != 0 {
		t.Errorf("%d prompt overrides left after reset", n)
	}
	after := status(t, s.get("/settings/prompts?reset=prompt_system"), http.StatusOK)
	mustContain(t, after, "Prompt reset", ">"+template.HTMLEscapeString(prompts.System)+"</textarea>")
	mustNotContain(t, after, "Reset to default")

	head, body = s.systemOfRun("again")
	if head != prompts.Preamble+"\n\n"+prompts.System {
		t.Errorf("reset system block differs from the default:\n%q", head)
	}
	if !strings.Contains(body, "Keep every detail below") {
		t.Error("reset summary instructions differ from the default")
	}
}

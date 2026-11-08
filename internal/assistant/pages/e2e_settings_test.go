package pages

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

var settingsTabs = []struct{ id, label string }{
	{"assistant", "Assistant"}, {"notifications", "Notifications"}, {"usage", "Usage"},
}

func TestSettingsIndexRedirectsAndTabsRender(t *testing.T) {
	s := newSite(t)
	index := s.get("/settings")
	if index.StatusCode != http.StatusSeeOther || index.Header.Get("Location") != "/settings/assistant" {
		t.Fatalf("/settings: %d %v", index.StatusCode, index.Header)
	}
	for _, tab := range settingsTabs {
		if tab.id == "notifications" {
			continue
		}
		page := status(t, s.get("/settings/"+tab.id), http.StatusOK)
		nav := page[strings.Index(page, `aria-label="Settings sections"`):]
		if !strings.Contains(nav, `class="-mb-2 flex flex-wrap gap-x-1 border-b text-sm"`) || strings.Contains(nav, "overflow-x-auto") {
			t.Errorf("%s: settings tabs do not wrap", tab.id)
		}
		nav = nav[:strings.Index(nav, "</nav>")]
		for _, other := range settingsTabs {
			if !strings.Contains(nav, `href="/settings/`+other.id+`"`) {
				t.Errorf("%s: no tab link to %s", tab.id, other.id)
			}
		}
		if n := strings.Count(nav, `aria-current="page"`); n != 1 {
			t.Errorf("%s: %d current tabs", tab.id, n)
		}
		current := nav[strings.Index(nav, `href="/settings/`+tab.id+`"`):]
		if !strings.Contains(current[:strings.Index(current, "</a>")], `aria-current="page"`) || !strings.Contains(current, `href="/settings/`+tab.id+`" class="`) {
			t.Errorf("%s: the current tab is not marked", tab.id)
		}
		mustContain(t, page, "Settings · "+tab.label, `<title>`+tab.label+` · Settings · atrium</title>`,
			`href="/settings" class="sb-item" title="Settings" aria-current="page"`)
	}
}

func TestOldSettingsUrlsAreGone(t *testing.T) {
	s := newSite(t)
	for _, path := range []string{"/usage", "/skills", "/schedules", "/notifications", "/settings/schedules", "/settings/skills", "/settings/subagents", "/settings/prompts"} {
		status(t, s.get(path), http.StatusNotFound)
	}
}

func TestAssistantSettings(t *testing.T) {
	s := newSite(t)
	page := status(t, s.get("/settings/assistant"), http.StatusOK)
	mustContain(t, page, `<option value="medium" selected>medium</option>`, `<option value="90000" selected>90K</option>`, `<option value="claude-haiku-5-5" selected>Haiku 5.5</option>`, `action="/settings/assistant"`)
	mustNotContain(t, page, "Defaults for new chat threads", "How readily", "Older tool results", "5×")
	savedResponse := s.post("/settings/assistant", url.Values{"effort": {"high"}, "context_limit": {"200000"}})
	if savedResponse.StatusCode != http.StatusSeeOther || savedResponse.Header.Get("Location") != "/settings/assistant?saved=1" || savedResponse.body != "" {
		t.Fatalf("assistant save: %d %q %q", savedResponse.StatusCode, savedResponse.Header.Get("Location"), savedResponse.body)
	}
	saved := status(t, s.get(savedResponse.Header.Get("Location")), http.StatusOK)
	mustContain(t, saved, "Saved", `<option value="high" selected>high</option>`, `<option value="200000" selected>200K</option>`, `data-category="success"`, `data-strip-params="saved"`)
	if n := strings.Count(saved, `id="toaster"`); n != 1 {
		t.Errorf("%d toasters", n)
	}
	mustNotContain(t, status(t, s.get("/settings/assistant"), http.StatusOK), "Saved")
	refused := status(t, s.post("/settings/assistant", url.Values{"effort": {"ultra"}, "context_limit": {"200000"}}), http.StatusUnprocessableEntity)
	mustContain(t, refused, "Not saved", "effort must be one of low, medium, high, xhigh, max", `data-category="error"`, `<p class="text-sm text-destructive" role="alert" tabindex="-1" autofocus>effort must be one of low, medium, high, xhigh, max</p>`, `<option value="high" selected>high</option>`)
	status(t, s.post("/settings/assistant", url.Values{"effort": {"high"}, "context_limit": {"7"}}), http.StatusUnprocessableEntity)
	created := s.multipart("/chat", map[string]string{"text": ""}, nil)
	if _, effort, limit := s.threadRow(s.idAt(created, "/chat/")); effort != "high" || limit != 200_000 {
		t.Errorf("new thread took %s %d", effort, limit)
	}
}

func TestUsagePage(t *testing.T) {
	s := newSite(t)
	empty := status(t, s.get("/settings/usage"), http.StatusOK)
	mustContain(t, empty, "No usage yet", "$0.00", "$150.00", `title="Unattended runs pause at 80%, chat at 100%">0% of $150 used</span>`, "Spent this month", "0 API calls")
	mustNotContain(t, empty, "Prompt tokens (all calls)", "Costs appear here")

	thread := s.newThread("", 0)
	s.exec("INSERT INTO runs (thread_id, kind, model, effort, state, started_at) VALUES (?, 'chat', 'm', 'medium', 'done', ?)", thread, s.clock)
	s.exec("INSERT INTO usage (ts, request_id, source, provider, thread_id, run_id, kind, model, effort, iterations, input_tokens, cache_write_5m, cache_write_1h, cache_read, output_tokens, searches, tier, cost_usd) "+
		"VALUES (?, lower(hex(randomblob(16))), 'loop', 'anthropic', ?, 1, 'chat', 'm', 'medium', '[]', 100, 0, 0, 900, 50, 2, '<=100K', 80)", s.clock, thread)
	page := status(t, s.get("/settings/usage"), http.StatusOK)
	mustContain(t, page, "$80.00", "$70.00", "53% of $150 used", "1 API calls", ">90%</span>", "900 read · 0 written · 100 uncached", "50 output tokens · 0 reasoning · 2 web searches", "Requests over 100K tokens",
		"$80.000", fmt.Sprintf(`href="/chat/%d"`, thread), "Untitled thread", "Prompt tokens (all calls)", "Largest request")
	mustNotContain(t, page, "No usage yet")
}

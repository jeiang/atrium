package pages

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

var settingsTabs = []struct{ id, label string }{
	{"assistant", "Assistant"}, {"skills", "Skills"}, {"subagents", "Subagents"}, {"notifications", "Notifications"}, {"usage", "Usage"},
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
			`href="/settings" class="btn" data-variant="ghost" data-size="icon-sm" aria-label="Settings" title="Settings" aria-current="page"`)
	}
}

func TestOldSettingsUrlsAreGone(t *testing.T) {
	s := newSite(t)
	for _, path := range []string{"/usage", "/skills", "/schedules", "/notifications", "/settings/schedules"} {
		status(t, s.get(path), http.StatusNotFound)
	}
}

func TestAssistantSettings(t *testing.T) {
	s := newSite(t)
	page := status(t, s.get("/settings/assistant"), http.StatusOK)
	mustContain(t, page, `<option value="medium" selected>medium</option>`, `<option value="90000" selected>90K tokens</option>`, `action="/settings/assistant"`, "Claude Haiku 5.5")
	saved := status(t, s.post("/settings/assistant", url.Values{"effort": {"high"}, "context_limit": {"200000"}}), http.StatusOK)
	mustContain(t, saved, "Saved", "New chat threads start with these defaults.", `<option value="high" selected>high</option>`, `<option value="200000" selected>200K tokens</option>`, `data-category="success"`)
	if n := strings.Count(saved, `id="toaster"`); n != 1 {
		t.Errorf("%d toasters", n)
	}
	refused := status(t, s.post("/settings/assistant", url.Values{"effort": {"ultra"}, "context_limit": {"200000"}}), http.StatusUnprocessableEntity)
	mustContain(t, refused, "Not saved", "effort must be one of low, medium, high, xhigh, max", `data-category="error"`, `<option value="high" selected>high</option>`)
	status(t, s.post("/settings/assistant", url.Values{"effort": {"high"}, "context_limit": {"7"}}), http.StatusUnprocessableEntity)
	created := s.multipart("/chat", map[string]string{"text": ""}, nil)
	if _, effort, limit := s.threadRow(s.idAt(created, "/chat/")); effort != "high" || limit != 200_000 {
		t.Errorf("new thread took %s %d", effort, limit)
	}
}

func TestUsagePage(t *testing.T) {
	s := newSite(t)
	empty := status(t, s.get("/settings/usage"), http.StatusOK)
	mustContain(t, empty, "No usage yet", "$0.00", "$75.00", "0% of $75 used; unattended runs pause at 80%, chat at 100%", "Spent this month", "0 API calls")
	mustNotContain(t, empty, "Prompt tokens (all calls)")

	thread := s.newThread("", 0)
	s.exec("INSERT INTO runs (thread_id, kind, model, effort, state, started_at) VALUES (?, 'chat', 'm', 'medium', 'done', ?)", thread, s.clock)
	s.exec("INSERT INTO usage (ts, thread_id, run_id, kind, model, effort, input_tokens, cache_write_5m, cache_write_1h, cache_read, output_tokens, searches, tier, cost_usd) "+
		"VALUES (?, ?, 1, 'chat', 'm', 'medium', 100, 0, 0, 900, 50, 2, '<=100K', 80)", s.clock, thread)
	page := status(t, s.get("/settings/usage"), http.StatusOK)
	mustContain(t, page, "$80.00", "$0.00", "107% of $75 used", "1 API calls", ">90%</span>", "900 read · 0 written · 100 uncached", "50 output tokens · 2 web searches", "Requests over 100K tokens",
		"$80.000", fmt.Sprintf(`href="/chat/%d"`, thread), "Untitled thread", "Prompt tokens (all calls)", "Largest request")
	mustNotContain(t, page, "No usage yet")
}

func TestSkillsEditor(t *testing.T) {
	s := newSite(t)
	mustContain(t, status(t, s.get("/settings/skills"), http.StatusOK), `data-skill="wiki-capture"`, "New skill", `action="/settings/skills"`)
	leaf := status(t, s.post("/settings/skills", url.Values{"name": {" b "}, "description": {"Bee"}, "body": {"leaf"}}), http.StatusOK)
	mustContain(t, leaf, "Skill saved", `data-skill="b"`, `data-category="success"`)
	saved := status(t, s.post("/settings/skills", url.Values{"name": {"a"}, "description": {"Ay"}, "body": {"see /skill:b and /skill:ghost"}}), http.StatusOK)
	mustContain(t, saved, "references skills that do not exist: ghost", `data-category="warning"`)
	page := status(t, s.get("/settings/skills"), http.StatusOK)
	mustContain(t, page, `data-skill="a"`, "unknown reference", `<dt>Referenced by</dt><dd><code class="font-mono">a</code>`, `title="References skills that do not exist: ghost"`, `formaction="/settings/skills/a/delete"`)

	s.exec("INSERT INTO skills (name, description, body, updated_at) VALUES ('weekly-review', 'Review the week', 'check the calendar', 1), ('trip-plan', 'Plan trips', 'book flights', 1)")
	byBody := status(t, s.get("/settings/skills?q=FLIGHTS"), http.StatusOK)
	mustContain(t, byBody, `data-skill="trip-plan"`, `value="FLIGHTS"`)
	mustNotContain(t, byBody, `data-skill="weekly-review"`)
	byDescription := status(t, s.get("/settings/skills?q=+review+the+"), http.StatusOK)
	mustContain(t, byDescription, `data-skill="weekly-review"`)
	mustNotContain(t, byDescription, `data-skill="trip-plan"`)
	none := status(t, s.get(`/settings/skills?q=<script>alert("xss")</script>`), http.StatusOK)
	mustContain(t, none, "No skills match", `Nothing matches &#34;&lt;script&gt;alert(&#34;xss&#34;)&lt;/script&gt;&#34;.`)
	mustNotContain(t, none, `data-skill="`, `<script>alert`)

	bad := status(t, s.post("/settings/skills", url.Values{"name": {"BAD"}, "description": {"d"}, "body": {"b"}}), http.StatusUnprocessableEntity)
	mustContain(t, bad, "Not saved", "name must be lowercase letters, digits, - or _ (max 48 characters)", `data-category="error"`, `<input id="new-name" name="name" type="text" value="BAD"`)
	status(t, s.post("/settings/skills", url.Values{"name": {"recipe"}, "description": {"Cooking steps"}, "body": {"# Cook"}}), http.StatusOK)
	mustContain(t, s.get("/settings/skills").body, "Cooking steps")
	deleted := status(t, s.post("/settings/skills/recipe/delete", nil), http.StatusOK)
	mustContain(t, deleted, "Skill deleted")
	mustNotContain(t, deleted, `data-skill="recipe"`)
	status(t, s.post("/settings/skills/recipe/delete", nil), http.StatusNotFound)
}

func TestSubagentsSettings(t *testing.T) {
	s := newSite(t)
	page := status(t, s.get("/settings/subagents"), http.StatusOK)
	mustContain(t, page, `data-agent="mail"`, `name="tools" value="mail_search"`, `name="tools" value="web_search"`, "built-in", "New subagent")
	mustNotContain(t, page, `value="notion_create_page"`)

	form := url.Values{"name": {"finance"}, "description": {"Spending and bills"}, "prompt": {"Use the ledger."}, "effort": {"high"}, "tools": {"mail_search", "contacts_lookup"}}
	made := status(t, s.post("/settings/subagents", form), http.StatusOK)
	mustContain(t, made, `data-agent="finance"`, "effort high", "Subagent saved", `data-enabled="true"`)
	var tools string
	if err := s.db.Read.QueryRow("SELECT tools_json FROM subagents WHERE name = 'finance'").Scan(&tools); err != nil || tools != `["contacts_lookup", "mail_search"]` {
		t.Errorf("stored tools %q (%v)", tools, err)
	}
	bad := status(t, s.post("/settings/subagents", url.Values{"name": {"evil"}, "description": {"d"}, "prompt": {"p"}, "effort": {"high"}, "tools": {"notion_create_page"}}), http.StatusUnprocessableEntity)
	mustContain(t, bad, "Not saved", "not read-only tools open to subagents: notion_create_page", `<input id="new-name" name="name" type="text" value="evil"`)
	if n := s.count("SELECT COUNT(*) FROM subagents WHERE name = 'evil'"); n != 0 {
		t.Errorf("refused agent was stored")
	}

	toggled := status(t, s.post("/settings/subagents/finance/toggle", nil), http.StatusOK)
	mustContain(t, toggled, "Subagent updated", `data-agent="finance" data-enabled="false"`, ">Enable</button>")
	if n := s.count("SELECT COUNT(*) FROM subagents WHERE name = 'finance' AND enabled = 0"); n != 1 {
		t.Error("toggle did not disable the agent")
	}
	edited := url.Values{"name": {"finance"}, "description": {"Money"}, "prompt": {"p"}, "effort": {"low"}, "tools": {"mail_search"}}
	status(t, s.post("/settings/subagents", edited), http.StatusOK)
	if n := s.count("SELECT COUNT(*) FROM subagents WHERE name = 'finance' AND enabled = 0 AND effort = 'low' AND description = 'Money'"); n != 1 {
		t.Error("editing changed the enabled flag or did not save")
	}
	status(t, s.post("/settings/subagents", url.Values{"name": {"plain"}, "description": {"d"}, "prompt": {"p"}, "tools": {"mail_search"}}), http.StatusOK)
	if n := s.count("SELECT COUNT(*) FROM subagents WHERE name = 'plain' AND effort = 'medium' AND enabled = 1"); n != 1 {
		t.Error("an absent effort did not default to medium")
	}

	deleted := status(t, s.post("/settings/subagents/finance/delete", nil), http.StatusOK)
	mustContain(t, deleted, "Subagent deleted")
	if n := s.count("SELECT COUNT(*) FROM subagents WHERE name = 'finance'"); n != 0 {
		t.Error("delete kept the agent")
	}
	builtin := status(t, s.post("/settings/subagents/mail/delete", nil), http.StatusUnprocessableEntity)
	mustContain(t, builtin, "Not deleted", "built-in agents cannot be deleted; disable instead")
	if n := s.count("SELECT COUNT(*) FROM subagents WHERE name = 'mail'"); n != 1 {
		t.Error("a built-in agent was deleted")
	}
	status(t, s.post("/settings/subagents/nope/toggle", nil), http.StatusNotFound)
	status(t, s.post("/settings/subagents/nope/delete", nil), http.StatusNotFound)
}

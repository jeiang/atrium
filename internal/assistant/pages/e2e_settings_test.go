package pages

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func assertNoConfirmOnAction(t *testing.T, page, action string) {
	t.Helper()
	marker := `formaction="` + action + `"`
	i := strings.Index(page, marker)
	if i < 0 {
		t.Fatalf("missing form action %q", action)
	}
	start := strings.LastIndex(page[:i], "<button ")
	end := strings.Index(page[i:], "</button>")
	if start < 0 || end < 0 {
		t.Fatalf("could not find button for action %q", action)
	}
	if strings.Contains(page[start:i+end], "data-confirm-submit=") {
		t.Errorf("%q unexpectedly requires confirmation", action)
	}
}

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
	savedResponse := s.post("/settings/assistant", url.Values{"effort": {"high"}, "context_limit": {"200000"}})
	if savedResponse.StatusCode != http.StatusSeeOther || savedResponse.Header.Get("Location") != "/settings/assistant?saved=1" || savedResponse.body != "" {
		t.Fatalf("assistant save: %d %q %q", savedResponse.StatusCode, savedResponse.Header.Get("Location"), savedResponse.body)
	}
	saved := status(t, s.get(savedResponse.Header.Get("Location")), http.StatusOK)
	mustContain(t, saved, "Saved", "New chat threads start with these defaults.", `<option value="high" selected>high</option>`, `<option value="200000" selected>200K tokens</option>`, `data-category="success"`, `data-strip-params="saved"`)
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
	mustContain(t, status(t, s.get("/settings/skills"), http.StatusOK), `data-skill="wiki-capture"`, "New skill", `action="/settings/skills"`, `data-confirm-submit="Delete the skill`)
	leafResponse := s.post("/settings/skills", url.Values{"name": {" b "}, "description": {"Bee"}, "body": {"leaf"}})
	if leafResponse.StatusCode != http.StatusSeeOther || leafResponse.Header.Get("Location") != "/settings/skills?saved=b" || leafResponse.body != "" {
		t.Fatalf("skill save: %d %q %q", leafResponse.StatusCode, leafResponse.Header.Get("Location"), leafResponse.body)
	}
	mustContain(t, status(t, s.get(leafResponse.Header.Get("Location")), http.StatusOK), "Skill saved", `data-skill="b"`, `data-strip-params="saved,deleted"`, `open`)
	savedResponse := s.post("/settings/skills", url.Values{"name": {"a"}, "description": {"Ay"}, "body": {"see /skill:b and /skill:ghost"}})
	if savedResponse.StatusCode != http.StatusSeeOther || savedResponse.Header.Get("Location") != "/settings/skills?saved=a" || savedResponse.body != "" {
		t.Fatalf("skill update: %d %q %q", savedResponse.StatusCode, savedResponse.Header.Get("Location"), savedResponse.body)
	}
	s.exec("INSERT INTO skills (name, description, body, updated_at) VALUES ('referrer', 'Refers to a', '/skill:a', 1)")
	saved := status(t, s.get(savedResponse.Header.Get("Location")), http.StatusOK)
	mustContain(t, saved, "references skills that do not exist: ghost", `data-category="warning"`, `data-skill="a" open`, "autofocus", `data-confirm-submit="Delete the skill “a”? referrer references it and will show an unknown reference. This cannot be undone."`)

	mustNotContain(t, status(t, s.get("/settings/skills?saved=nope"), http.StatusOK), `data-category=`)
	mustNotContain(t, status(t, s.get("/settings/skills?deleted=a"), http.StatusOK), `data-category=`)

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
	mustContain(t, bad, "Not saved", "name must be lowercase letters, digits, - or _ (max 48 characters)", `data-category="error"`, `<input id="new-name" name="name" type="text" value="BAD"`, `<p class="text-sm text-destructive" role="alert" tabindex="-1" autofocus>name must be lowercase`)
	editBad := status(t, s.post("/settings/skills", url.Values{"name": {"a"}, "description": {"Draft description"}, "body": {""}, "from": {"edit"}}), http.StatusUnprocessableEntity)
	mustContain(t, editBad, `data-skill="a" open`, `value="Draft description"`, `<textarea id="b-a" name="body" rows="10" class="w-full font-mono text-xs"></textarea>`, `role="alert"`)
	newBad := status(t, s.post("/settings/skills", url.Values{"name": {"a"}, "description": {"New draft"}, "body": {""}}), http.StatusUnprocessableEntity)
	mustContain(t, newBad, `data-skill="a"`, `value="a"`, `value="New draft"`)
	if strings.Contains(newBad[strings.Index(newBad, `data-skill="a"`):], `data-skill="a" open`) {
		t.Error("existing skill opened for a new-form validation error")
	}

	recipe := s.post("/settings/skills", url.Values{"name": {"recipe"}, "description": {"Cooking steps"}, "body": {"# Cook"}})
	if recipe.StatusCode != http.StatusSeeOther {
		t.Fatalf("recipe save: %d %s", recipe.StatusCode, recipe.body)
	}
	mustContain(t, s.get("/settings/skills?saved=recipe").body, "Cooking steps")
	deleted := s.post("/settings/skills/recipe/delete", nil)
	if deleted.StatusCode != http.StatusSeeOther || deleted.Header.Get("Location") != "/settings/skills?deleted=recipe" || deleted.body != "" {
		t.Fatalf("skill delete: %d %q %q", deleted.StatusCode, deleted.Header.Get("Location"), deleted.body)
	}
	deletedPage := status(t, s.get(deleted.Header.Get("Location")), http.StatusOK)
	mustContain(t, deletedPage, "Skill deleted", `data-strip-params="saved,deleted"`)
	mustNotContain(t, deletedPage, `data-skill="recipe"`)
	status(t, s.post("/settings/skills/recipe/delete", nil), http.StatusNotFound)
}

func TestSubagentsSettings(t *testing.T) {
	s := newSite(t)
	page := status(t, s.get("/settings/subagents"), http.StatusOK)
	mustContain(t, page, `data-agent="mail"`, `name="tools" value="mail_search"`, `name="tools" value="web_search"`, "built-in")
	mustNotContain(t, page, `name="tools" value="notion_create_page"`)
	mustContain(t, page, `<nav aria-label="Settings sections" class="-mb-2 flex flex-wrap gap-x-1 border-b text-sm">`)

	form := url.Values{"name": {"finance"}, "description": {"Spending and bills"}, "prompt": {"Use the ledger."}, "effort": {"high"}, "tools": {"mail_search", "contacts_lookup"}}
	madeResponse := s.post("/settings/subagents", form)
	if madeResponse.StatusCode != http.StatusSeeOther || madeResponse.Header.Get("Location") != "/settings/subagents?saved=finance" || madeResponse.body != "" {
		t.Fatalf("subagent save: %d %q %q", madeResponse.StatusCode, madeResponse.Header.Get("Location"), madeResponse.body)
	}
	made := status(t, s.get(madeResponse.Header.Get("Location")), http.StatusOK)
	mustContain(t, made, `data-agent="finance"`, "effort high", "Subagent saved", `data-enabled="true"`, `data-strip-params="saved,deleted,toggled"`, `data-agent="finance" data-enabled="true" open`, "autofocus", `data-confirm-submit="Delete the subagent “finance”? The assistant can no longer delegate to it. This cannot be undone."`)
	assertNoConfirmOnAction(t, made, "/settings/subagents/finance/toggle")
	var tools string
	if err := s.db.Read.QueryRow("SELECT tools_json FROM subagents WHERE name = 'finance'").Scan(&tools); err != nil || tools != `["contacts_lookup", "mail_search"]` {
		t.Errorf("stored tools %q (%v)", tools, err)
	}
	mustNotContain(t, status(t, s.get("/settings/subagents?saved=nope"), http.StatusOK), `data-category=`)
	mustNotContain(t, status(t, s.get("/settings/subagents?deleted=finance"), http.StatusOK), `data-category=`)
	mustNotContain(t, status(t, s.get("/settings/subagents?toggled=nope"), http.StatusOK), `data-category=`)
	special := "fin&ance #1 Café"
	s.exec("INSERT INTO subagents (name, description, prompt, effort, tools_json, enabled, builtin, updated_at) VALUES (?, ?, ?, 'medium', '[]', 1, 0, 1)", special, "Special name", "Research.")
	specialToggle := s.post("/settings/subagents/"+url.PathEscape(special)+"/toggle", nil)
	specialLocation := "/settings/subagents?toggled=" + url.QueryEscape(special)
	if specialToggle.StatusCode != http.StatusSeeOther || specialToggle.Header.Get("Location") != specialLocation || specialToggle.body != "" {
		t.Fatalf("special subagent toggle: %d %q %q", specialToggle.StatusCode, specialToggle.Header.Get("Location"), specialToggle.body)
	}
	specialPage := status(t, s.get(specialLocation), http.StatusOK)
	mustContain(t, specialPage, "Subagent updated", `data-category="success"`, `data-agent="fin&amp;ance #1 Café"`)

	assertNoConfirmOnAction(t, page, "/settings/subagents/mail/toggle")
	bad := status(t, s.post("/settings/subagents", url.Values{"name": {"evil"}, "description": {"d"}, "prompt": {"p"}, "effort": {"high"}, "tools": {"notion_create_page"}}), http.StatusUnprocessableEntity)
	mustContain(t, bad, "Not saved", "not read-only tools open to subagents: notion_create_page", `<input id="new-name" name="name" type="text" value="evil"`, `<p class="text-sm text-destructive" role="alert" tabindex="-1" autofocus>not read-only tools`)
	if n := s.count("SELECT COUNT(*) FROM subagents WHERE name = 'evil'"); n != 0 {
		t.Errorf("refused agent was stored")
	}

	editBad := status(t, s.post("/settings/subagents", url.Values{"name": {"finance"}, "description": {"Draft description"}, "prompt": {"Draft prompt"}, "effort": {"low"}, "tools": {"mail_search", "notion_create_page"}, "from": {"edit"}}), http.StatusUnprocessableEntity)
	mustContain(t, editBad, `data-agent="finance" data-enabled="true" open`, `value="Draft description"`, `>Draft prompt</textarea>`, `name="tools" value="mail_search" class="input" checked>`, `role="alert"`)
	unknownEdit := status(t, s.post("/settings/subagents", url.Values{"name": {"missing"}, "description": {"Draft"}, "prompt": {"Draft"}, "effort": {"low"}, "tools": {"notion_create_page"}, "from": {"edit"}}), http.StatusUnprocessableEntity)
	mustContain(t, unknownEdit, "New subagent", `id="new-name" name="name" type="text" value="missing"`, `role="alert"`)
	mustNotContain(t, unknownEdit, `data-agent="missing"`)
	newBad := status(t, s.post("/settings/subagents", url.Values{"name": {"finance"}, "description": {"New draft"}, "prompt": {"Draft"}, "effort": {"low"}, "tools": {"mail_search", "notion_create_page"}}), http.StatusUnprocessableEntity)
	mustContain(t, newBad, `value="finance"`, `value="New draft"`, `>Draft</textarea>`)
	if strings.Contains(newBad[strings.Index(newBad, `data-agent="finance"`):], `data-agent="finance" data-enabled="true" open`) {
		t.Error("existing subagent opened for a new-form validation error")
	}

	toggledResponse := s.post("/settings/subagents/finance/toggle", nil)
	if toggledResponse.StatusCode != http.StatusSeeOther || toggledResponse.Header.Get("Location") != "/settings/subagents?toggled=finance" || toggledResponse.body != "" {
		t.Fatalf("subagent toggle: %d %q %q", toggledResponse.StatusCode, toggledResponse.Header.Get("Location"), toggledResponse.body)
	}
	toggled := status(t, s.get(toggledResponse.Header.Get("Location")), http.StatusOK)
	mustContain(t, toggled, "Subagent updated", "finance is now disabled", `data-agent="finance" data-enabled="false"`, ">Enable</button>")
	assertNoConfirmOnAction(t, toggled, "/settings/subagents/finance/toggle")
	if n := s.count("SELECT COUNT(*) FROM subagents WHERE name = 'finance' AND enabled = 0"); n != 1 {
		t.Error("toggle did not disable the agent")
	}
	edited := url.Values{"name": {"finance"}, "description": {"Money"}, "prompt": {"p"}, "effort": {"low"}, "tools": {"mail_search"}}
	editedResponse := s.post("/settings/subagents", edited)
	if editedResponse.StatusCode != http.StatusSeeOther {
		t.Fatalf("subagent edit: %d %s", editedResponse.StatusCode, editedResponse.body)
	}
	if n := s.count("SELECT COUNT(*) FROM subagents WHERE name = 'finance' AND enabled = 0 AND effort = 'low' AND description = 'Money'"); n != 1 {
		t.Error("editing changed the enabled flag or did not save")
	}
	plain := s.post("/settings/subagents", url.Values{"name": {"plain"}, "description": {"d"}, "prompt": {"p"}, "tools": {"mail_search"}})
	if plain.StatusCode != http.StatusSeeOther {
		t.Fatalf("subagent create without effort: %d %s", plain.StatusCode, plain.body)
	}
	if n := s.count("SELECT COUNT(*) FROM subagents WHERE name = 'plain' AND effort = 'medium' AND enabled = 1"); n != 1 {
		t.Error("an absent effort did not default to medium")
	}

	deletedResponse := s.post("/settings/subagents/finance/delete", nil)
	if deletedResponse.StatusCode != http.StatusSeeOther || deletedResponse.Header.Get("Location") != "/settings/subagents?deleted=finance" || deletedResponse.body != "" {
		t.Fatalf("subagent delete: %d %q %q", deletedResponse.StatusCode, deletedResponse.Header.Get("Location"), deletedResponse.body)
	}
	deleted := status(t, s.get(deletedResponse.Header.Get("Location")), http.StatusOK)
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

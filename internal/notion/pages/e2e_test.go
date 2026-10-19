package pages

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/assistant/projects"
	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/httpx"
)

const unsafeMarkdown = "# Plan\n\n<script>alert(1)</script>\n\n[bad](javascript:alert(1)) [ok](https://example.test/x) " +
	"[page](notion://page/1) [rel](/relative)\n\n<img src=x onerror=alert(2)>\n"

var notionMarkdown = "Intro line\nSecond line\n" +
	`<page url="https://app.notion.com/p/` + strings.ReplaceAll(secretID, "-", "") + `">Child</page>` + "\n" +
	`See <mention-page url="https://app.notion.com/p/` + strings.ReplaceAll(secretID, "-", "") + `"/> soon.` + "\n" +
	"<empty-block/>\n" +
	`## Tasks {color="blue"}` + "\n" +
	"- [ ] Open\n- [x] Done\n" +
	"<table header-row=\"true\">\n\t<tr>\n\t\t<td>Bill</td>\n\t\t<td>Cost</td>\n\t</tr>\n" +
	"\t<tr>\n\t\t<td>Phone</td>\n\t\t<td>1<br>2</td>\n\t</tr>\n</table>\n" +
	"<callout icon=\"x\">\n\tCallout text\n</callout>\n" +
	"```\n<page url=\"https://example.test\">kept</page>\n```\n"

func TestNotionPageMarkdownIsSanitized(t *testing.T) {
	s := newSite(t, nil)
	s.addPage(pageID, "Plan", unsafeMarkdown, 0)
	body := status(t, s.get("/notion/"+pageID), http.StatusOK)
	mustNotContain(t, body, "<script>alert", "<img src=x", `href="javascript:`, `href="/relative"`)
	mustContain(t, body, "&lt;script&gt;", `href="https://example.test/x"`, `rel="noopener noreferrer"`, `href="notion://page/1"`)
	status(t, s.get("/notion/00000000-0000-0000-0000-000000000000"), http.StatusNotFound)
}

func TestNotionPageRendersNotionFlavouredMarkdown(t *testing.T) {
	s := newSite(t, nil)
	s.addPage(pageID, "Plan", notionMarkdown, 0)
	s.addPage(secretID, "Budget", "x", 0)
	body := status(t, s.get("/notion/"+pageID), http.StatusOK)
	before, _, _ := strings.Cut(body, "<pre>")
	mustNotContain(t, before, "&lt;page")
	mustNotContain(t, body, "empty-block", "{color=")
	mustContain(t, body,
		"Intro line<br>",
		`<a href="/notion/`+secretID+`" rel="noopener noreferrer">Child</a>`,
		`See <a href="/notion/`+secretID+`" rel="noopener noreferrer">Budget</a> soon.`,
		"<h2>Tasks</h2>",
		`<input class="input" type="checkbox" disabled> Open`,
		`<input class="input" type="checkbox" disabled checked> Done`,
		"<th>Bill</th>", "<td>1 2</td>", "<p>Callout text</p>", "<pre>", "&lt;page url=",
	)
}

func TestNotionPagePropertiesArePlain(t *testing.T) {
	s := newSite(t, nil)
	s.addPage(pageID, "Plan", notionMarkdown, 0)
	s.addPage(secretID, "Budget", "x", 0)
	s.exec("UPDATE notion_pages SET properties = ? WHERE id = ?",
		`{"Links": {"type": "relation", "relation": []}, "Amount": {"type": "number", "number": 5}}`, pageID)
	body := status(t, s.get("/notion/"+pageID), http.StatusOK)
	mustContain(t, body, "Amount")
	mustNotContain(t, body, "Links", "0 linked")
}

func (s *site) answer(reply string) {
	s.chat = func(string, string) (json.RawMessage, error) { return json.RawMessage(reply), nil }
}

func TestNotionActionsCreateProposalsOrReportErrors(t *testing.T) {
	s := newSite(t, nil)
	s.addPage(pageID, "Plan", "text", 0)
	s.answer(`{}`)

	missing := status(t, s.post("/notion/"+pageID+"/link", form("target", secretID), hx, "true"), http.StatusOK)
	mustContain(t, missing, "unknown page "+secretID, "No proposal")
	empty := status(t, s.post("/notion/"+pageID+"/edit", form("instruction", " "), hx, "true"), http.StatusOK)
	mustContain(t, empty, "empty instruction")
	unusable := status(t, s.post("/notion/"+pageID+"/edit", form("instruction", "shorten"), hx, "true"), http.StatusOK)
	mustContain(t, unusable, "the model proposed no applicable edit")

	s.answer(`{"updates": [{"old_str": "text", "new_str": "shorter text"}], "summary": "shorten"}`)
	made := status(t, s.post("/notion/"+pageID+"/edit", form("instruction", "shorten")), http.StatusOK)
	mustContain(t, made, "Review proposal #1", "Edit Plan", "Notion edit proposed", `href="/proposals?kind=notion_edit#proposal-1"`)
	var kind, title, state string
	if err := s.db.Read.QueryRow("SELECT kind, title, status FROM proposals WHERE id = 1").Scan(&kind, &title, &state); err != nil {
		t.Fatal(err)
	}
	if kind != "notion_edit" || title != "Edit Plan" || state != "pending" {
		t.Errorf("proposal = %q %q %q", kind, title, state)
	}

	s.addPage(secretID, "Budget", "y", 0)
	s.answer(`{"link": false, "reason": "unrelated"}`)
	none := status(t, s.post("/notion/"+pageID+"/link", form("target", secretID), hx, "true"), http.StatusOK)
	mustContain(t, none, "No proposal was made", `data-variant="destructive"`)
	s.answer(`{"link": true, "reason": "same project"}`)
	linked := status(t, s.post("/notion/"+pageID+"/link", form("target", secretID), hx, "true"), http.StatusOK)
	mustContain(t, linked, "Review proposal #2", "Link Plan to Budget", "Notion link proposed")

	s.chat = func(string, string) (json.RawMessage, error) {
		return nil, &httpx.ServiceUnavailable{Msg: "chat model is down"}
	}
	down := status(t, s.post("/notion/"+pageID+"/edit", form("instruction", "again"), hx, "true"), http.StatusOK)
	mustContain(t, down, "chat model is down", `role="alert"`)

	rec := s.post("/notion/"+pageID+"/link", nil, hx, "true")
	status(t, rec, http.StatusUnprocessableEntity)
	mustContain(t, rec.Body.String(), `"loc":["body","target"]`, `"type":"missing"`)
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q", got)
	}
}

func TestProposeLinkDisablesEveryLinkButtonWhileRunning(t *testing.T) {
	s := newSite(t, nil)
	s.addPage(pageID, "Plan", "x", 0)
	s.addPage(secretID, "Budget", "y", 0)
	s.addVector(1, unit(0, 1, 0))
	s.addVector(2, unit(0, 1, 0.5))
	body := status(t, s.get("/notion/"+pageID), http.StatusOK)
	mustContain(t, body, `id="related-list"`, `hx-disabled-elt="#related-list button"`, `aria-label="Propose a link to Budget"`,
		`hx-post="/notion/`+pageID+`/link"`, `href="/notion/`+secretID+`"`)
}

func TestNotionPageIsAFragmentForHTMXAndPageOtherwise(t *testing.T) {
	s := newSite(t, nil)
	s.addPage(pageID, "Plan", "text", 0)
	s.addPage(secretID, "Budget", "y", 0)
	s.addVector(1, unit(0, 1, 0))
	s.addVector(2, unit(0, 1, 0.5))

	partial := status(t, s.get("/notion/"+pageID, hx, "true"), http.StatusOK)
	mustContain(t, partial, `<article`, `id="page-title"`)
	mustNotContain(t, partial, "<aside", "related-list", "<html", "Draft an edit")

	restored := status(t, s.get("/notion/"+pageID, hx, "true", "HX-History-Restore-Request", "true"), http.StatusOK)
	mustContain(t, restored, "<html", "<aside", `id="related-list"`)

	full := status(t, s.get("/notion/"+strings.ReplaceAll(pageID, "-", "")), http.StatusOK)
	mustContain(t, full, "<html", `id="edit-result"`, `id="link-result"`, `<template id="action-loading">`, "Open in Notion")

	unknown := status(t, s.get("/notion/nothing"), http.StatusNotFound)
	mustContain(t, unknown, "Nothing here", "<html")
	notFragment := status(t, s.get("/notion/nothing", hx, "true"), http.StatusNotFound)
	mustContain(t, notFragment, "Nothing here")
	mustNotContain(t, notFragment, "<html")
	status(t, s.post("/projects/999/wiki-hub", nil, hx, "true"), http.StatusNotFound)
	status(t, s.post("/projects/abc/wiki-hub", nil, hx, "true"), http.StatusUnprocessableEntity)
}

func TestNotionPageBackLink(t *testing.T) {
	s := newSite(t, nil)
	s.addPage(pageID, "Plan", "text", 0)
	for _, tc := range []struct {
		referer, href, label string
	}{
		{"http://testserver/chat?x=1", `href="/chat?x=1"`, "Chat"},
		{"http://testserver/triage", `href="/triage"`, "Decisions"},
		{"http://testserver/triage?stage=llm&q=a", `href="/triage?stage=llm&amp;q=a"`, "Decisions"},
		{"http://elsewhere.test/triage", `href="/chat"`, "Chat"},
		{"http://testserver/notion/other", `href="/chat"`, "Chat"},
		{"", `href="/chat"`, "Chat"},
	} {
		body := status(t, s.get("/notion/"+pageID, "Referer", tc.referer), http.StatusOK)
		_, rest, found := strings.Cut(body, `<a `+tc.href+` class="btn self-start" data-size="sm" data-variant="ghost">`)
		if label, _, _ := strings.Cut(rest, "</a>"); !found || !strings.HasSuffix(label, tc.label) {
			t.Errorf("referer %q: want a back link %s labelled %q in %.300s", tc.referer, tc.href, tc.label, body)
		}
	}
}

func TestCreateWikiHubLinksTheNewPage(t *testing.T) {
	fake := newFakeNotion(t)
	s := newSite(t, map[string]string{"NOTION_TOKEN": notionToken, "ATRIUM_NOTION_WIKI_ROOT": wikiRootID})
	id := s.newProject("Always answer in metric units.")
	edit := "/projects/" + id + "/edit"
	mustContain(t, status(t, s.get(edit), http.StatusOK), "Create wiki hub")

	reply := status(t, s.post("/projects/"+id+"/wiki-hub", nil, hx, "true"), http.StatusOK)
	mustContain(t, reply, "Wiki hub created", `id="project-hub"`)
	hub := s.hubOf(id)
	if hub == nil {
		t.Fatal("the project has no wiki hub")
	}
	if parent := fake.parentOf(*hub); parent != wikiRootID {
		t.Errorf("hub page parent = %q, want the wiki root", parent)
	}
	again := status(t, s.get(edit), http.StatusOK)
	mustContain(t, again, *hub)
	mustNotContain(t, again, "Create wiki hub")

	var threadID, runID *int64
	if err := s.db.Read.QueryRow("SELECT thread_id, run_id FROM agent_writes WHERE tool = 'notion_create_page'").Scan(&threadID, &runID); err != nil {
		t.Fatal(err)
	}
	if threadID != nil || runID != nil {
		t.Errorf("hub write belongs to thread %v run %v, want neither", threadID, runID)
	}

	project := s.projectOf(id)
	block, err := projects.SystemBlock(t.Context(), s.db.Read, threads.Thread{ProjectID: &project.ID})
	if err != nil || block == nil || !strings.Contains(*block, "wiki-capture") {
		t.Errorf("system block = %v, %v; want it to name wiki-capture", block, err)
	}

	second := status(t, s.post("/projects/"+id+"/wiki-hub", nil, hx, "true"), http.StatusOK)
	mustContain(t, second, *hub)
	mustNotContain(t, second, "Wiki hub created", "Create wiki hub")
	if n := len(fake.idsTitled("Kitchen")); n != 1 {
		t.Errorf("%d Kitchen pages in Notion, want 1", n)
	}
}

func TestCreateWikiHubReportsAMissingWikiRoot(t *testing.T) {
	s := newSite(t, nil)
	id := s.newProject("x")
	reply := status(t, s.post("/projects/"+id+"/wiki-hub", nil, hx, "true"), http.StatusUnprocessableEntity)
	mustContain(t, reply, "ATRIUM_NOTION_WIKI_ROOT", `id="project-hub"`, "Create wiki hub", `role="alert"`)
	if hub := s.hubOf(id); hub != nil {
		t.Errorf("wiki_hub_id = %q, want NULL", *hub)
	}
}

func TestCreateWikiHubReportsNotionRefusals(t *testing.T) {
	fake := newFakeNotion(t)
	fake.refuse = true
	s := newSite(t, map[string]string{"NOTION_TOKEN": notionToken, "ATRIUM_NOTION_WIKI_ROOT": wikiRootID})
	id := s.newProject("x")
	reply := status(t, s.post("/projects/"+id+"/wiki-hub", nil, hx, "true"), http.StatusUnprocessableEntity)
	mustContain(t, reply, "Notion refused the page", "Create wiki hub")
	if hub := s.hubOf(id); hub != nil {
		t.Errorf("wiki_hub_id = %q, want NULL", *hub)
	}
	var writes int
	if err := s.db.Read.QueryRow("SELECT COUNT(*) FROM agent_writes").Scan(&writes); err != nil || writes != 0 {
		t.Errorf("agent_writes = %d, %v; a refused create writes nothing", writes, err)
	}
}

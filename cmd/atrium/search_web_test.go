package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	asstapp "github.com/jeiang/atrium/internal/assistant/application"
	"github.com/jeiang/atrium/internal/assistant/webfetch"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/contacts"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/tool"
)

func webSearchReply(query string, results ...anthropictest.SearchResult) anthropictest.Response {
	return anthropictest.Reply("pause_turn", anthropictest.WebSearch("ws1", query, results...)...)
}

func searchResult(t *testing.T, r anthropictest.Request, id string) map[string]any {
	t.Helper()
	result := toolResultFor(t, r, id)
	if result["is_error"] == true {
		t.Fatalf("search_web returned an error: %v", result)
	}
	var out map[string]any
	text, _ := result["content"].(string)
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("search_web result: %v\n%s", err, text)
	}
	return out
}

func reportSchema(t *testing.T, r anthropictest.Request) map[string]any {
	t.Helper()
	for _, raw := range r.JSON(t)["tools"].([]any) {
		entry, _ := raw.(map[string]any)
		if entry["name"] != "report" {
			continue
		}
		schema, _ := entry["input_schema"].(map[string]any)
		return schema
	}
	t.Fatal("the request has no report tool")
	return nil
}

func TestSearchWebRunsAnIsolatedChildAndVerifiesCitations(t *testing.T) {
	srv, pages := webPages(t)
	dir, api, f := moduleFixture(t, flagsExcept("", ""))
	const home = "Maple Street 12"
	warm(t, api)
	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("p1", "profile_set", map[string]any{"key": "home", "value": home})))
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("saved")))
	if ran := runApp(t, "agent", "run", "--text", "remember where I live"); ran.code != cli.ExitOK {
		t.Fatalf("profile run: %d %s", ran.code, ran.stderr)
	}
	start := len(api.Requests())
	api.Push(
		anthropictest.Reply("tool_use", anthropictest.ToolUse("s1", "search_web", map[string]any{
			"query": "library hours", "question": "When does the library open?", "max_sources": 4,
		})),
		webSearchReply("library hours",
			anthropictest.SearchResult{URL: srv.URL + "/a", Title: "A"},
			anthropictest.SearchResult{URL: srv.URL + "/b", Title: "B"},
			anthropictest.SearchResult{URL: srv.URL + "/c", Title: "C"}),
		anthropictest.Reply("tool_use",
			anthropictest.ToolUse("f1", "web_fetch", map[string]any{"url": srv.URL + "/a"}),
			anthropictest.ToolUse("f2", "web_fetch", map[string]any{"url": srv.URL + "/c"})),
		anthropictest.Reply("tool_use", anthropictest.ToolUse("r1", "report", map[string]any{"answer": map[string]any{
			"answer": "The library opens at nine [1]. Page B is [2]. Page C is [3]. Bogus [4].",
			"citations": []any{
				map[string]any{"n": 1, "url": srv.URL + "/a", "quote": pageAMarker},
				map[string]any{"n": 2, "url": srv.URL + "/b", "quote": "not fetched"},
				map[string]any{"n": 3, "url": srv.URL + "/c", "quote": nil},
				map[string]any{"n": 4, "url": "https://example.invalid/x", "quote": "nope"},
			},
		}})),
		anthropictest.Reply("end_turn", anthropictest.Text("done")),
		anthropictest.Reply("end_turn", anthropictest.Text("ok")),
	)
	if ran := runApp(t, "agent", "run", "--text", "when does the library open"); ran.code != cli.ExitOK {
		t.Fatalf("search run: %d %s", ran.code, ran.stderr)
	}
	requests := api.Requests()[start:]
	if len(requests) != 6 {
		t.Fatalf("%d model requests, want 6", len(requests))
	}
	child := requests[1]
	if system := systemText(t, child); strings.Contains(system, home) || strings.Contains(system, "Operator profile") {
		t.Errorf("the search child carries the profile: %s", system)
	}
	if system := systemText(t, child); !strings.Contains(system, "web search reader") || !strings.Contains(system, "untrusted") {
		t.Errorf("the search child lacks the reader prompt: %s", system)
	}
	childTools := requestTools(t, child)
	for _, name := range []string{"web_search", "web_fetch", "recall", "report"} {
		if !slices.Contains(childTools, name) {
			t.Errorf("the search child lacks %s: %v", name, childTools)
		}
	}
	for _, name := range []string{"mail_search", "task_add", "notify", "search_threads"} {
		if slices.Contains(childTools, name) {
			t.Errorf("the search child was offered %s: %v", name, childTools)
		}
	}
	for _, raw := range child.JSON(t)["tools"].([]any) {
		entry, _ := raw.(map[string]any)
		if entry["name"] == "web_search" && entry["max_uses"] != float64(5) {
			t.Errorf("web_search max_uses = %v, want 5", entry["max_uses"])
		}
	}
	schema := reportSchema(t, child)
	props, _ := schema["properties"].(map[string]any)
	answer, _ := props["answer"].(map[string]any)
	answerProps, _ := answer["properties"].(map[string]any)
	if _, ok := answerProps["answer"]; !ok {
		t.Errorf("the report schema lacks the answer field: %v", answerProps)
	}
	if required := fmt.Sprint(answer["required"]); !strings.Contains(required, "answer") || !strings.Contains(required, "citations") {
		t.Errorf("the report schema required = %v, want answer and citations", answer["required"])
	}
	citations, _ := answerProps["citations"].(map[string]any)
	if citations["maxItems"] != float64(4) {
		t.Errorf("the report schema citations maxItems = %v, want 4", citations["maxItems"])
	}
	items, _ := citations["items"].(map[string]any)
	itemProps, _ := items["properties"].(map[string]any)
	for _, name := range []string{"n", "url", "quote"} {
		if _, ok := itemProps[name]; !ok {
			t.Errorf("the report schema lacks citation field %s: %v", name, itemProps)
		}
	}
	result := searchResult(t, requests[5], "s1")
	answerText, _ := result["answer"].(string)
	if strings.Contains(answerText, "[4]") {
		t.Errorf("the dropped citation marker survived: %q", answerText)
	}
	if !strings.Contains(answerText, "[1]") || !strings.Contains(answerText, "[2]") || !strings.Contains(answerText, "[3]") {
		t.Errorf("the surviving citation markers are missing: %q", answerText)
	}
	sources, _ := result["sources"].([]any)
	if len(sources) != 3 {
		t.Fatalf("sources = %v, want 3", sources)
	}
	verified := map[float64]string{}
	for _, raw := range sources {
		source, _ := raw.(map[string]any)
		n, _ := source["n"].(float64)
		verified[n], _ = source["verified"].(string)
	}
	if verified[1] != "fetched" || verified[2] != "search" || verified[3] != "search" {
		t.Errorf("verified = %v, want 1 fetched and 2,3 search", verified)
	}
	hits := pages.seen()
	slices.Sort(hits)
	if !slices.Equal(hits, []string{"/a", "/c"}) {
		t.Errorf("page fetches = %v, want /a and /c", hits)
	}
	childID, _ := result["child_thread_id"].(float64)
	parent := parentOf(t, dir, int64(childID))
	if marks := threadMarkSet(t, dir, parent); marks != int64(tool.Untrusted) {
		t.Errorf("parent marks = %d, want untrusted", marks)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), db.Key{})
	defer func() { _ = d.Close() }()
	if got := operationAudits(t, d, "assistant.search-web"); len(got) != 1 || !strings.HasPrefix(got[0], fmt.Sprintf("ok thread:%d ", int64(childID))) {
		t.Errorf("search_web audits: %q", got)
	}
	var raw string
	if err := d.Read.QueryRow("SELECT allowed_urls FROM threads WHERE id = ?", parent).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var allowed []string
	if err := json.Unmarshal([]byte(raw), &allowed); err != nil {
		t.Fatal(err)
	}
	normalized := map[string]struct{}{}
	for _, u := range allowed {
		if n, err := webfetch.NormalizeURL(u); err == nil {
			normalized[n] = struct{}{}
		}
	}
	for _, want := range []string{srv.URL + "/a", srv.URL + "/b", srv.URL + "/c"} {
		n, _ := webfetch.NormalizeURL(want)
		if _, ok := normalized[n]; !ok {
			t.Errorf("parent allowlist lacks %s: %v", want, allowed)
		}
	}
	if _, ok := normalized["https://example.invalid/x"]; ok {
		t.Errorf("the dropped citation URL reached the parent allowlist: %v", allowed)
	}
	base, _, _ := startServeApp(t, freshApp(f), &lockedBuffer{})
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if status, _, _ := fetch(t, client, "GET", base+"/chat", nil); status != http.StatusOK {
		t.Fatalf("GET /chat = %d", status)
	}
	if status, _, _ := fetch(t, client, "GET", fmt.Sprintf("%s/chat/%d", base, parent), nil); status != http.StatusOK {
		t.Fatalf("GET the parent thread = %d", status)
	}
}

func TestSearchWebDropsMarkersWithoutCitations(t *testing.T) {
	srv, _ := webPages(t)
	_, api, _ := moduleFixture(t, flagsExcept("", ""))
	start := warm(t, api)
	api.Push(
		anthropictest.Reply("tool_use", anthropictest.ToolUse("s1", "search_web", map[string]any{
			"query": "library hours", "max_sources": 3,
		})),
		webSearchReply("library hours", anthropictest.SearchResult{URL: srv.URL + "/a", Title: "A"}),
		anthropictest.Reply("tool_use", anthropictest.ToolUse("r1", "report", map[string]any{"answer": map[string]any{
			"answer":    "The library opens at nine [1].",
			"citations": nil,
		}})),
		anthropictest.Reply("end_turn", anthropictest.Text("done")),
		anthropictest.Reply("end_turn", anthropictest.Text("ok")),
	)
	if ran := runApp(t, "agent", "run", "--text", "when does the library open"); ran.code != cli.ExitOK {
		t.Fatalf("search run: %d %s", ran.code, ran.stderr)
	}
	requests := api.Requests()[start:]
	if len(requests) != 5 {
		t.Fatalf("%d model requests, want 5", len(requests))
	}
	result := searchResult(t, requests[4], "s1")
	answer, _ := result["answer"].(string)
	if strings.Contains(answer, "[1]") {
		t.Errorf("the unsourced citation marker survived: %q", answer)
	}
	if result["unsourced"] != true {
		t.Errorf("unsourced = %v, want true", result["unsourced"])
	}
	sources, _ := result["sources"].([]any)
	if len(sources) != 0 {
		t.Errorf("sources = %v, want none", sources)
	}
}

func TestSearchWebAsksInAMarkedChat(t *testing.T) {
	t.Run("refused after mail", func(t *testing.T) {
		dir, api, _ := moduleFixture(t, flagsExcept("", ""))
		start := warm(t, api)
		message := seedMail(t, dir)
		api.Push(
			anthropictest.Reply("tool_use", anthropictest.ToolUse("m1", "mail_read", map[string]any{"message_id": message})),
			anthropictest.Reply("tool_use", anthropictest.ToolUse("s1", "search_web", map[string]any{"query": "the code"})),
			anthropictest.Reply("end_turn", anthropictest.Text("done")),
		)
		if ran := runApp(t, "agent", "run", "--text", "read my mail then search"); ran.code != cli.ExitOK {
			t.Fatalf("run: %d %s", ran.code, ran.stderr)
		}
		requests := api.Requests()[start:]
		if len(requests) != 3 {
			t.Fatalf("%d model requests, want 3", len(requests))
		}
		refusal := toolResultFor(t, requests[2], "s1")
		if refusal["is_error"] != true || !strings.Contains(fmt.Sprint(refusal["content"]), "not allowed in this thread by the permission table") {
			t.Errorf("search_web after mail returned %v, want a refusal", refusal)
		}
		d := mustConnect(t, filepath.Join(dir, "atrium.db"), db.Key{})
		defer func() { _ = d.Close() }()
		var proposals int
		if err := d.Read.QueryRow("SELECT COUNT(*) FROM proposals WHERE kind = 'permission'").Scan(&proposals); err != nil || proposals != 0 {
			t.Errorf("permission proposals = %d, %v, want none", proposals, err)
		}
	})
	t.Run("allowed after contact names", func(t *testing.T) {
		dir, api, _ := moduleFixture(t, flagsExcept("", ""))
		contactsDir := filepath.Join(dir, "contacts")
		setEnv(t, settingEnv(contacts.DirKey), contactsDir)
		if err := os.MkdirAll(filepath.Join(contactsDir, "card"), 0o777); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(contactsDir, "card", "alice.vcf"), []byte(lookupCard), 0o666); err != nil {
			t.Fatal(err)
		}
		start := warm(t, api)
		api.Push(
			anthropictest.Reply("tool_use", anthropictest.ToolUse("c1", "contacts_lookup", map[string]any{"query": "Alice"})),
			anthropictest.Reply("tool_use", anthropictest.ToolUse("s1", "search_web", map[string]any{"query": "Example Corp"})),
			webSearchReply("Example Corp", anthropictest.SearchResult{URL: "https://example.com/a", Title: "A"}),
			anthropictest.Reply("tool_use", anthropictest.ToolUse("r1", "report", map[string]any{"answer": map[string]any{"answer": "", "citations": []any{}}})),
			anthropictest.Reply("end_turn", anthropictest.Text("done")),
			anthropictest.Reply("end_turn", anthropictest.Text("ok")),
		)
		if ran := runApp(t, "agent", "run", "--text", "where does Alice work"); ran.code != cli.ExitOK {
			t.Fatalf("run: %d %s", ran.code, ran.stderr)
		}
		requests := api.Requests()[start:]
		if len(requests) != 6 {
			t.Fatalf("%d model requests, want 6", len(requests))
		}
		if result := toolResultFor(t, requests[5], "s1"); result["is_error"] == true {
			t.Errorf("search_web after a name-only contact read failed: %v", result)
		}
	})
	t.Run("asks after search_threads", func(t *testing.T) {
		dir, api, f := moduleFixture(t, flagsExcept("", ""))
		warm(t, api)
		thread := latestThread(t, dir)
		base, _, _ := startServeApp(t, freshApp(f), &lockedBuffer{})
		jar, err := cookiejar.New(nil)
		if err != nil {
			t.Fatal(err)
		}
		client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		if status, _, _ := fetch(t, client, "GET", base+"/chat", nil); status != http.StatusOK {
			t.Fatalf("GET /chat = %d", status)
		}
		start := len(api.Requests())
		api.Push(
			anthropictest.Reply("tool_use", anthropictest.ToolUse("t1", "search_threads", map[string]any{"query": "warm"})),
			anthropictest.Reply("tool_use", anthropictest.ToolUse("s1", "search_web", map[string]any{"query": "the warm thread", "question": "what did it say?"})),
			webSearchReply("the warm thread", anthropictest.SearchResult{URL: "https://example.com/a", Title: "A"}),
			anthropictest.Reply("tool_use", anthropictest.ToolUse("r1", "report", map[string]any{"answer": map[string]any{"answer": "", "citations": []any{}}})),
			anthropictest.Reply("end_turn", anthropictest.Text("done")),
			anthropictest.Reply("end_turn", anthropictest.Text("ok")),
		)
		if status, body, _ := fetch(t, client, "POST", fmt.Sprintf("%s/chat/%d/messages", base, thread), url.Values{"text": {"search the web for the warm thread"}}); status >= http.StatusBadRequest {
			t.Fatalf("POST message = %d: %s", status, body)
		}
		d := mustConnect(t, filepath.Join(dir, "atrium.db"), db.Key{})
		defer func() { _ = d.Close() }()
		proposal, _ := waitParkedPermission(t, d, thread)
		code, card := hxFetch(t, client, http.MethodGet, base+fmt.Sprintf("/proposals/%d/card", proposal), nil)
		if code != http.StatusOK {
			t.Fatalf("GET the card = %d\n%s", code, card)
		}
		if code, body := hxFetch(t, client, http.MethodPost, base+fmt.Sprintf("/proposals/%d/approve", proposal), url.Values{"all": {"1"}}); code != http.StatusOK {
			t.Fatalf("approve = %d\n%s", code, body)
		}
		waitRunState(t, d, thread, "done")
		requests := api.Requests()[start:]
		if len(requests) != 6 {
			t.Fatalf("%d model requests, want 6", len(requests))
		}
		if result := toolResultFor(t, requests[5], "s1"); result["is_error"] == true {
			t.Errorf("the approved search failed: %v", result)
		}
	})
}

func TestChatNoLongerSeesServerWebSearch(t *testing.T) {
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	warm(t, api)
	requests := api.Requests()
	if len(requests) == 0 {
		t.Fatal("the chat run sent no request")
	}
	tools := requestTools(t, requests[0])
	if slices.Contains(tools, "web_search") {
		t.Errorf("the chat request still offers the server web_search: %v", tools)
	}
	if !slices.Contains(tools, "search_web") {
		t.Errorf("the chat request lacks search_web: %v", tools)
	}
	ran := runApp(t, "agent", "subagents", "list")
	if ran.code != cli.ExitOK {
		t.Fatalf("subagents list: %d %s", ran.code, ran.stderr)
	}
	if strings.Contains(ran.stdout, `"name": "web"`) {
		t.Errorf("the retired web role is still listed: %s", ran.stdout)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), db.Key{})
	defer func() { _ = d.Close() }()
	var n int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM subagents WHERE name = 'web' AND builtin = 1").Scan(&n); err != nil || n != 0 {
		t.Errorf("builtin web rows = %d, %v, want none", n, err)
	}
}

func TestSearchWebStopsAtItsCostCap(t *testing.T) {
	_, api, _ := moduleFixture(t, flagsExcept("", ""))
	seedSettings(t, map[string]string{asstapp.KeySearchWebMaxCostUSD: "0.0001"})
	start := warm(t, api)
	api.Push(
		anthropictest.Reply("tool_use", anthropictest.ToolUse("s1", "search_web", map[string]any{"query": "atrium"})),
		anthropictest.Reply("end_turn", anthropictest.Text("done")),
	)
	if ran := runApp(t, "agent", "run", "--text", "search"); ran.code != cli.ExitOK {
		t.Fatalf("run: %d %s", ran.code, ran.stderr)
	}
	requests := api.Requests()[start:]
	if len(requests) != 2 {
		t.Fatalf("%d model requests, want only the parent's two", len(requests))
	}
	result := toolResultFor(t, requests[1], "s1")
	if result["is_error"] != true || !strings.Contains(fmt.Sprint(result["content"]), "cost") {
		t.Errorf("search_web at its cost cap returned %v, want a cost error", result)
	}
}

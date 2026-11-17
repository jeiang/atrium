package main

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func byTitle(t *testing.T, items []any) map[string]map[string]any {
	t.Helper()
	out := map[string]map[string]any{}
	for _, item := range items {
		m := item.(map[string]any)
		out[m["title"].(string)] = m
	}
	return out
}

func TestKnowledgeSubjectPages(t *testing.T) {
	t.Parallel()
	s, token, d := localAPISite(t)
	person := apiData(t, s.do(http.MethodPost, "/api/v1/knowledge", token, map[string]any{
		"title": "Ana Lima", "aliases": []string{"Ana"}, "scope": "personal",
		"body": "# Ana Lima\nFriend from the climbing gym.\nMet in 2024.\n\n## Contact\n- Prefers Signal\n- Lives in Porto\n\n1. Call first\n> Ask before visiting\n```\ncode 77\n```",
	}), http.StatusCreated)
	ana := number(t, person["id"])
	insert := func(title, body, extra string, args ...any) {
		t.Helper()
		cols, vals := "title, body, scope, writer, created_at, updated_at", "?, ?, 'personal', ?, ?, ?"
		if extra != "" {
			cols, vals = cols+", "+extra, vals+strings.Repeat(", ?", strings.Count(extra, ",")+1)
		}
		if _, err := d.Write.Exec("INSERT INTO kb_records ("+cols+") VALUES ("+vals+")", append([]any{title, body}, args...)...); err != nil {
			t.Fatal(err)
		}
	}
	insert("Ana prefers oolong tea", "Ana Lima drinks oolong in the afternoon.", "source_thread_id, source_seq", "chat", 2000, 2000, 11, 3)
	insert("Birthday", "Ana's birthday is 3 May.", "source_thread_id, source_run_id", "dreaming", 2100, 2100, 12, 7)
	insert("Ana prefers green tea", "Ana drinks green tea.", "source_thread_id, retracted_at", "chat", 1500, 1500, 11, 1900.0)
	insert("Ana's door code", "Ana's door code is 1234.", "private", "operator", 2200, 2200, 1)
	insert("Porto", "A city in Portugal.", "", "operator", 2300, 2300)
	insert("Lesson: confirm before moving mail", "Ask first.", "source_run_id", "dreaming", 2400, 2400, 7)
	insert("Climbing gym", "Bouldering on Tuesdays.", "origin", "import", 2500, 2500, "notion:abc123")

	pagePath := fmt.Sprintf("/api/v1/knowledge/%d/page", ana)
	tag := expectConditional(t, s, token, pagePath)
	page := apiData(t, s.do(http.MethodGet, pagePath, token, nil), http.StatusOK)
	if subject := page["subject"].(map[string]any); subject["title"] != "Ana Lima" || page["kind"] != "fact" || page["facts_truncated"] != false {
		t.Fatalf("subject: %v", page)
	}
	facts := byTitle(t, page["facts"].([]any))
	if len(facts) != 4 {
		t.Fatalf("facts: %v", page["facts"])
	}
	oolong, birthday := facts["Ana prefers oolong tea"], facts["Birthday"]
	if src := oolong["source"].(map[string]any); number(t, src["thread_id"]) != 11 || number(t, src["seq"]) != 3 || oolong["value"] != "Ana Lima drinks oolong in the afternoon." ||
		oolong["superseded"] != false || oolong["learned_at"] == nil || oolong["writer"] != "chat" {
		t.Fatalf("oolong fact: %v", oolong)
	}
	if src := birthday["source"].(map[string]any); number(t, src["thread_id"]) != 12 || number(t, src["run_id"]) != 7 || birthday["writer"] != "dreaming" {
		t.Fatalf("birthday fact: %v", birthday)
	}
	if old := facts["Ana prefers green tea"]; old["superseded"] != true || old["superseded_at"] == nil {
		t.Fatalf("superseded fact: %v", old)
	}
	if private := facts["Ana's door code"]; private["private"] != true || private["value"] != "Ana's door code is 1234." {
		t.Fatalf("private fact: %v", private)
	}
	if _, ok := facts["Porto"]; ok {
		t.Fatal("a record the subject mentions is listed as its fact")
	}
	related := byTitle(t, page["related"].([]any))
	if porto := related["Porto"]; porto == nil || porto["relation"] != "mentions" {
		t.Fatalf("related: %v", page["related"])
	}

	sections := page["sections"].([]any)
	if len(sections) != 2 {
		t.Fatalf("sections: %v", sections)
	}
	lede, contact := sections[0].(map[string]any), sections[1].(map[string]any)
	if lede["heading"] != "Ana Lima" || number(t, lede["level"]) != 1 || contact["heading"] != "Contact" || number(t, contact["level"]) != 2 {
		t.Fatalf("section headings: %v", sections)
	}
	var kinds, texts []string
	for _, b := range contact["blocks"].([]any) {
		block := b.(map[string]any)
		kinds, texts = append(kinds, block["kind"].(string)), append(texts, block["text"].(string))
	}
	if strings.Join(kinds, ",") != "item,item,numbered,quote,code" || strings.Join(texts, "|") != "Prefers Signal|Lives in Porto|Call first|Ask before visiting|code 77" {
		t.Fatalf("contact blocks: %v %v", kinds, texts)
	}
	if block := lede["blocks"].([]any)[0].(map[string]any); block["kind"] != "paragraph" || block["text"] != "Friend from the climbing gym.\nMet in 2024." {
		t.Fatalf("lede: %v", lede)
	}
	if raw := string(s.do(http.MethodGet, pagePath, token, nil).body); strings.Contains(raw, "<") || strings.Contains(raw, `\u003c`) {
		t.Fatalf("page carries markup: %.300s", raw)
	}

	apiData(t, s.do(http.MethodPost, "/api/v1/knowledge", token, map[string]any{
		"title": "Ana's cat", "body": "Ana has a cat called Miso.", "scope": "personal",
	}), http.StatusCreated)
	if changed := expectChanged(t, s, token, pagePath, tag); len(changed["facts"].([]any)) != 5 {
		t.Fatalf("new fact missing from the page: %v", changed["facts"])
	}

	subjects := byTitle(t, apiItems(t, apiData(t, s.do(http.MethodGet, "/api/v1/knowledge/subjects?q=ana", token, nil), http.StatusOK)))
	if row := subjects["Ana Lima"]; row == nil || number(t, row["facts"]) != 4 || row["kind"] != "fact" {
		t.Fatalf("subjects by title: %v", subjects)
	}
	if _, ok := subjects["Ana prefers green tea"]; ok || subjects["Ana's door code"]["private"] != true || subjects["Birthday"] != nil {
		t.Fatalf("subjects by title: %v", subjects)
	}
	for kind, want := range map[string]string{"lesson": "Lesson: confirm before moving mail", "wiki": "Climbing gym"} {
		items := apiItems(t, apiData(t, s.do(http.MethodGet, "/api/v1/knowledge/subjects?kind="+kind, token, nil), http.StatusOK))
		if len(items) != 1 || items[0].(map[string]any)["title"] != want || items[0].(map[string]any)["kind"] != kind {
			t.Fatalf("kind %s: %v", kind, items)
		}
	}
	if facts := byTitle(t, apiItems(t, apiData(t, s.do(http.MethodGet, "/api/v1/knowledge/subjects?kind=fact", token, nil), http.StatusOK))); facts["Climbing gym"] != nil || facts["Ana Lima"] == nil {
		t.Fatalf("kind fact: %v", facts)
	}
	expectField(t, s.do(http.MethodGet, "/api/v1/knowledge/subjects?kind=person", token, nil), "kind")
	expectAPI(t, s.do(http.MethodGet, "/api/v1/knowledge/999999/page", token, nil), http.StatusNotFound, "not_found")
	expectAPI(t, s.do(http.MethodGet, "/api/v1/knowledge/999999/map", token, nil), http.StatusNotFound, "not_found")

	mapPath := fmt.Sprintf("/api/v1/knowledge/%d/map", ana)
	near := apiData(t, s.do(http.MethodGet, mapPath, token, nil), http.StatusOK)
	nodes := byTitle(t, near["nodes"].([]any))
	if near["truncated"] != false || number(t, near["cap"]) != 50 || number(t, near["center"]) != ana || nodes["Ana prefers green tea"] != nil ||
		number(t, nodes["Birthday"]["depth"]) != 1 || nodes["Lesson: confirm before moving mail"] == nil || number(t, nodes["Lesson: confirm before moving mail"]["depth"]) != 2 {
		t.Fatalf("map: %v", near)
	}
	edgeKinds := map[string]bool{}
	for _, e := range near["edges"].([]any) {
		edgeKinds[e.(map[string]any)["kind"].(string)] = true
	}
	if !edgeKinds["mentions"] || !edgeKinds["same_run"] {
		t.Fatalf("edges: %v", near["edges"])
	}
	if one := byTitle(t, apiData(t, s.do(http.MethodGet, mapPath+"?depth=1", token, nil), http.StatusOK)["nodes"].([]any)); one["Lesson: confirm before moving mail"] != nil {
		t.Fatalf("depth 1 reached two steps: %v", one)
	}
	expectField(t, s.do(http.MethodGet, mapPath+"?depth=3", token, nil), "depth")

	for i := range 60 {
		insert(fmt.Sprintf("Note %02d", i), "Ana said something.", "", "chat", 3000+i, 3000+i)
	}
	bounded := apiData(t, s.do(http.MethodGet, mapPath, token, nil), http.StatusOK)
	if len(bounded["nodes"].([]any)) != 50 || bounded["truncated"] != true {
		t.Fatalf("bounded map: %d nodes, truncated %v", len(bounded["nodes"].([]any)), bounded["truncated"])
	}
}

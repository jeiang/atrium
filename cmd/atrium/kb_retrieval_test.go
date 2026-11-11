package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/cli"
)

func knowledgeMessage(t *testing.T, request anthropictest.Request) (string, bool) {
	t.Helper()
	messages, _ := request.JSON(t)["messages"].([]any)
	for i := len(messages) - 1; i >= 0; i-- {
		message, ok := messages[i].(map[string]any)
		if !ok || message["role"] != "user" {
			continue
		}
		raw, err := json.Marshal(message["content"])
		if err != nil {
			t.Fatal(err)
		}
		var blocks []map[string]any
		if json.Unmarshal(raw, &blocks) != nil {
			continue
		}
		textAt, knowledgeAt := -1, -1
		for at, block := range blocks {
			text, _ := block["text"].(string)
			switch {
			case strings.Contains(text, "Knowledge records matched to this message"):
				knowledgeAt = at
			case strings.Contains(text, "When is the dentist"):
				textAt = at
			}
		}
		if knowledgeAt >= 0 {
			if textAt < 0 || knowledgeAt < textAt {
				t.Errorf("the knowledge block is not after the operator text: %v", blocks)
			}
			text, _ := blocks[knowledgeAt]["text"].(string)
			return text, true
		}
	}
	return "", false
}

func TestChatRunCarriesRetrievedKnowledgeWithCitations(t *testing.T) {
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	if ran := runApp(t, "agent", "run", "--text", "warm"); ran.code != cli.ExitOK {
		t.Fatalf("warm run: %d %s", ran.code, ran.stderr)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
	defer func() { _ = d.Close() }()
	base, _, stop := startServeStoppable(t, dir)
	defer stop()
	w := asstBrowse(t, base)

	dentist := knowledgeCreate(t, w, url.Values{
		"title": {"Dentist"}, "body": {"Dr Ng at the Main St clinic; prefers mornings."}, "scope": {"personal"},
	})
	if code, body, _ := w.post(fmt.Sprintf("/library/knowledge/%d/confirm", dentist), url.Values{}, false); code != http.StatusSeeOther {
		t.Fatalf("confirm = %d: %s", code, body)
	}
	knowledgeCreate(t, w, url.Values{
		"title": {"Rumour"}, "body": {"The clinic moved to Elm St."}, "scope": {"personal"}, "untrusted": {"1"},
	})
	code, body, header := w.post("/projects", url.Values{
		"name": {"Household"}, "effort": {"medium"}, "context_limit": {"90000"}, "private": {"1"},
	}, false)
	if code != http.StatusSeeOther {
		t.Fatalf("create project = %d: %s", code, body)
	}
	projectID, err := strconv.ParseInt(strings.TrimSuffix(strings.TrimPrefix(header.Get("Location"), "/projects/"), "/edit"), 10, 64)
	if err != nil {
		t.Fatalf("project redirect %q: %v", header.Get("Location"), err)
	}
	boiler := knowledgeCreate(t, w, url.Values{
		"title": {"Boiler code"}, "body": {"The boiler code is 4417."}, "scope": {"work"},
	})
	if _, err := d.Write.Exec("UPDATE kb_records SET project_id = ? WHERE id = ?", projectID, boiler); err != nil {
		t.Fatal(err)
	}

	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("hi")))
	start := len(api.Requests())
	if ran := runApp(t, "agent", "run", "--text", "hello"); ran.code != cli.ExitOK {
		t.Fatalf("run without retrieval: %d %s", ran.code, ran.stderr)
	}
	off := api.Requests()[start:]
	if len(off) != 1 {
		t.Fatalf("%d requests without retrieval, want 1", len(off))
	}

	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("t1", "task_list", map[string]any{})))
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text(fmt.Sprintf("See [K%d].", dentist))))
	start = len(api.Requests())
	if ran := runApp(t, "agent", "run", "--text", "When is the dentist, the rumour, and the boiler code?"); ran.code != cli.ExitOK {
		t.Fatalf("run with retrieval: %d %s", ran.code, ran.stderr)
	}
	on := api.Requests()[start:]
	if len(on) != 2 {
		t.Fatalf("%d requests with retrieval, want 2", len(on))
	}

	first := on[0].JSON(t)
	offTools, _ := json.Marshal(off[0].JSON(t)["tools"])
	onTools, _ := json.Marshal(first["tools"])
	if !bytes.Equal(offTools, onTools) {
		t.Errorf("tools differ with retrieval on:\n%s\n%s", offTools, onTools)
	}
	offSystem, _ := json.Marshal(off[0].JSON(t)["system"])
	onSystem, _ := json.Marshal(first["system"])
	if !bytes.Equal(offSystem, onSystem) {
		t.Errorf("system blocks differ with retrieval on:\n%s\n%s", offSystem, onSystem)
	}

	one, ok := knowledgeMessage(t, on[0])
	if !ok {
		t.Fatalf("the operator message carries no knowledge block: %v", first["messages"])
	}
	two, ok := knowledgeMessage(t, on[1])
	if !ok || one != two {
		t.Errorf("the knowledge block changed between steps:\n%s\n%s", one, two)
	}
	if !strings.Contains(one, fmt.Sprintf("[K%d] Dentist", dentist)) {
		t.Errorf("knowledge block lacks the confirmed record: %s", one)
	}
	for _, bad := range []string{"Rumour", "Boiler code", "Elm St", "4417"} {
		if strings.Contains(one, bad) {
			t.Errorf("knowledge block carries ineligible %q: %s", bad, one)
		}
	}

	thread := latestThread(t, dir)
	if marks := kbMarks(t, d, thread); marks != 0 {
		t.Errorf("thread marks after retrieval = %d, want none", marks)
	}
	code, page, _ := w.get(fmt.Sprintf("/chat/%d", thread), false)
	if code != http.StatusOK {
		t.Fatalf("thread page = %d", code)
	}
	if !strings.Contains(page, fmt.Sprintf(`href="/library/knowledge/%d"`, dentist)) {
		t.Errorf("transcript lacks the knowledge chip link: %.600s", page)
	}
	if !strings.Contains(page, fmt.Sprintf(`href="/library/knowledge/%d">[K%d]</a>`, dentist, dentist)) {
		t.Errorf("assistant prose does not link [K%d]: %.600s", dentist, page)
	}
}

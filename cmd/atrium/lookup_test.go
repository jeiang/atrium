package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/contacts"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/tool"
)

const lookupCard = "BEGIN:VCARD\r\nVERSION:3.0\r\nUID:alice\r\nFN:Alice Example\r\nORG:Example Corp\r\nEMAIL:alice@example.com\r\nEND:VCARD\r\n"

func seedLookupMail(t *testing.T, dir string) int64 {
	t.Helper()
	path := filepath.Join(dir, "atrium.db")
	d := mustConnect(t, path)
	t.Cleanup(func() { _ = d.Close() })
	if err := db.Migrate(context.Background(), d, path); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.Exec("INSERT INTO accounts (name, kind) VALUES ('icloud', 'icloud')"); err != nil {
		t.Fatal(err)
	}
	res, err := d.Write.Exec("INSERT INTO mailboxes (account, name, imap_name, role, uidvalidity, highest_uid) VALUES ('icloud', 'INBOX', 'INBOX', 'inbox', 7, 1)")
	if err != nil {
		t.Fatal(err)
	}
	mailbox, _ := res.LastInsertId()
	raw := "From: Bank <bank@example.com>\r\nTo: Owner <owner@example.com>\r\nSubject: Your statement\r\n\r\nYour statement is ready.\r\n"
	rel := "raw/lookup/1.eml"
	file := filepath.Join(dir, "cache", filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(file), 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(raw), 0o666); err != nil {
		t.Fatal(err)
	}
	res, err = d.Write.Exec("INSERT INTO messages (account, mailbox_id, uidvalidity, uid, msgid, digest, date_ts, subject, from_addr, raw_path, first_seen) "+
		"VALUES ('icloud', ?, 7, 1, 'lookup@x', 'd', ?, 'Your statement', 'bank@example.com', ?, ?)",
		mailbox, float64(time.Date(2026, 1, 2, 9, 0, 0, 0, time.UTC).Unix()), rel, float64(time.Now().Unix()))
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

func lookupResult(t *testing.T, r anthropictest.Request, id string) map[string]any {
	t.Helper()
	result := toolResultFor(t, r, id)
	if result["is_error"] == true {
		t.Fatalf("lookup returned an error: %v", result)
	}
	var out map[string]any
	text, _ := result["content"].(string)
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("lookup result: %v\n%s", err, text)
	}
	return out
}

func TestLookupReturnsTypedFieldsWithoutUntrusted(t *testing.T) {
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	start := warm(t, api)
	message := seedLookupMail(t, dir)
	api.Push(
		anthropictest.Reply("tool_use", anthropictest.ToolUse("l1", "lookup", map[string]any{
			"source": "mail", "question": "When was the statement sent and is it urgent?",
			"fields": []any{
				map[string]any{"name": "when", "type": "date"},
				map[string]any{"name": "urgent", "type": "boolean"},
			},
		})),
		anthropictest.Reply("tool_use", anthropictest.ToolUse("m1", "mail_read", map[string]any{"message_id": message})),
		anthropictest.Reply("tool_use", anthropictest.ToolUse("r1", "report", map[string]any{"answer": map[string]any{"when": "2026-01-02", "urgent": false}})),
		anthropictest.Reply("end_turn", anthropictest.Text("reported")),
		anthropictest.Reply("tool_use", anthropictest.ToolUse("f1", "web_fetch", map[string]any{"url": "https://example.com/"})),
		anthropictest.Reply("end_turn", anthropictest.Text("done")),
	)
	if ran := runApp(t, "agent", "run", "--text", "look up the statement"); ran.code != cli.ExitOK {
		t.Fatalf("lookup run: %d %s", ran.code, ran.stderr)
	}
	requests := api.Requests()[start:]
	if len(requests) != 6 {
		t.Fatalf("%d model requests, want 6", len(requests))
	}
	childTools := requestTools(t, requests[1])
	for _, name := range []string{"mail_search", "mail_read", "contacts_lookup", "recall", "report"} {
		if !slices.Contains(childTools, name) {
			t.Errorf("the lookup child lacks %s: %v", name, childTools)
		}
	}
	for _, name := range []string{"web_fetch", "web_search", "task_add", "notify"} {
		if slices.Contains(childTools, name) {
			t.Errorf("the lookup child was offered %s: %v", name, childTools)
		}
	}
	if system := systemText(t, requests[1]); !strings.Contains(system, "Fields:") || !strings.Contains(system, "- when: date") {
		t.Errorf("the lookup child system prompt lacks the field list: %s", system)
	}
	result := lookupResult(t, requests[4], "l1")
	answer, _ := result["answer"].(map[string]any)
	if answer["when"] != "2026-01-02" || answer["urgent"] != false {
		t.Errorf("lookup answer = %v", answer)
	}
	child, _ := result["child_thread_id"].(float64)
	parent := parentOf(t, dir, int64(child))
	if marks := threadMarkSet(t, dir, parent); marks != int64(tool.PII|tool.Messages) {
		t.Errorf("parent marks = %d, want pii|messages without untrusted", marks)
	}
	refusal := toolResultFor(t, requests[5], "f1")
	if refusal["is_error"] != true || !strings.Contains(fmt.Sprint(refusal["content"]), "not allowed in this thread by the permission table") {
		t.Errorf("web_fetch after a pii lookup returned %v, want a refusal", refusal)
	}
}

func TestLookupTextFieldKeepsUntrusted(t *testing.T) {
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	start := warm(t, api)
	message := seedLookupMail(t, dir)
	api.Push(
		anthropictest.Reply("tool_use", anthropictest.ToolUse("l1", "lookup", map[string]any{
			"source": "mail", "question": "What does the statement say?",
			"fields": []any{map[string]any{"name": "summary", "type": "text"}},
		})),
		anthropictest.Reply("tool_use", anthropictest.ToolUse("m1", "mail_read", map[string]any{"message_id": message})),
		anthropictest.Reply("tool_use", anthropictest.ToolUse("r1", "report", map[string]any{"answer": map[string]any{"summary": "Your statement is ready."}})),
		anthropictest.Reply("end_turn", anthropictest.Text("reported")),
		anthropictest.Reply("tool_use", anthropictest.ToolUse("f1", "web_fetch", map[string]any{"url": "https://example.com/"})),
		anthropictest.Reply("end_turn", anthropictest.Text("done")),
	)
	if ran := runApp(t, "agent", "run", "--text", "look up the statement"); ran.code != cli.ExitOK {
		t.Fatalf("lookup run: %d %s", ran.code, ran.stderr)
	}
	requests := api.Requests()[start:]
	if len(requests) != 6 {
		t.Fatalf("%d model requests, want 6", len(requests))
	}
	result := lookupResult(t, requests[5], "l1")
	answer, _ := result["answer"].(map[string]any)
	if answer["summary"] != "Your statement is ready." {
		t.Errorf("lookup answer = %v", answer)
	}
	fields, _ := result["text_fields"].([]any)
	if len(fields) != 1 || fields[0] != "summary" {
		t.Errorf("text_fields = %v", fields)
	}
	child, _ := result["child_thread_id"].(float64)
	parent := parentOf(t, dir, int64(child))
	if marks := threadMarkSet(t, dir, parent); marks != int64(tool.PII|tool.Messages|tool.Untrusted) {
		t.Errorf("parent marks = %d, want pii|messages|untrusted", marks)
	}
	refusal := toolResultFor(t, requests[5], "f1")
	if refusal["is_error"] != true || !strings.Contains(fmt.Sprint(refusal["content"]), "not allowed in this thread by the permission table") {
		t.Errorf("web_fetch after an untrusted lookup returned %v, want a refusal", refusal)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
	defer func() { _ = d.Close() }()
	var proposals int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM proposals WHERE kind = 'permission'").Scan(&proposals); err != nil || proposals != 0 {
		t.Errorf("permission proposals = %d, %v, want none", proposals, err)
	}
}

func TestLookupRefusesInvalidReportAndForcesOne(t *testing.T) {
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	start := warm(t, api)
	message := seedLookupMail(t, dir)
	api.Push(
		anthropictest.Reply("tool_use", anthropictest.ToolUse("l1", "lookup", map[string]any{
			"source": "mail", "question": "Is the statement open and which message is it?",
			"fields": []any{
				map[string]any{"name": "status", "type": "enum", "values": []any{"open", "closed"}},
				map[string]any{"name": "message", "type": "id", "id_of": "mail_message"},
			},
		})),
		anthropictest.Reply("tool_use", anthropictest.ToolUse("m1", "mail_read", map[string]any{"message_id": message})),
		anthropictest.Reply("tool_use", anthropictest.ToolUse("r1", "report", map[string]any{"answer": map[string]any{"status": "maybe", "message": 999}})),
		anthropictest.Reply("end_turn", anthropictest.Text("done")),
		anthropictest.Reply("tool_use", anthropictest.ToolUse("r2", "report", map[string]any{"answer": map[string]any{"status": "open", "message": message}})),
		anthropictest.Reply("end_turn", anthropictest.Text("ok")),
	)
	if ran := runApp(t, "agent", "run", "--text", "look up the statement"); ran.code != cli.ExitOK {
		t.Fatalf("lookup run: %d %s", ran.code, ran.stderr)
	}
	requests := api.Requests()[start:]
	if len(requests) != 6 {
		t.Fatalf("%d model requests, want 6", len(requests))
	}
	invalid := toolResultFor(t, requests[3], "r1")
	if invalid["is_error"] != true {
		t.Fatalf("the invalid report was accepted: %v", invalid)
	}
	for _, want := range []string{"status", "message"} {
		if !strings.Contains(fmt.Sprint(invalid["content"]), want) {
			t.Errorf("the invalid report error lacks %q: %v", want, invalid["content"])
		}
	}
	first := requests[1].JSON(t)
	if _, ok := first["thinking"]; !ok {
		t.Error("the lookup child's first request has no thinking")
	}
	forced := requests[4].JSON(t)
	choice, _ := forced["tool_choice"].(map[string]any)
	if choice["type"] != "tool" || choice["name"] != "report" {
		t.Errorf("the forced request tool_choice = %v, want report", choice)
	}
	if _, ok := forced["thinking"]; ok {
		t.Error("the forced request still sends thinking")
	}
	result := lookupResult(t, requests[5], "l1")
	answer, _ := result["answer"].(map[string]any)
	if answer["status"] != "open" || answer["message"] != float64(message) {
		t.Errorf("lookup answer = %v", answer)
	}
	child, _ := result["child_thread_id"].(float64)
	parent := parentOf(t, dir, int64(child))
	if marks := threadMarkSet(t, dir, parent); marks != int64(tool.PII|tool.Messages) {
		t.Errorf("parent marks = %d, want pii|messages from the id field", marks)
	}
}

func TestContactNameLookupKeepsTheInternetOpen(t *testing.T) {
	srv, pages := webPages(t)
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
	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
	now := float64(time.Now().Unix())
	res, err := d.Write.Exec("INSERT INTO threads (kind, effort, created_at, updated_at, allowed_urls) VALUES ('chat', 'medium', ?, ?, ?)",
		now, now, fmt.Sprintf(`[%q]`, srv.URL+"/b"))
	if err != nil {
		t.Fatal(err)
	}
	thread, _ := res.LastInsertId()
	_ = d.Close()
	api.Push(
		anthropictest.Reply("tool_use", anthropictest.ToolUse("l1", "lookup", map[string]any{
			"source": "contacts", "question": "Where does Alice work?",
			"fields": []any{map[string]any{"name": "employer", "type": "text"}},
		})),
		anthropictest.Reply("tool_use", anthropictest.ToolUse("c1", "contacts_lookup", map[string]any{"query": "Alice"})),
		anthropictest.Reply("tool_use", anthropictest.ToolUse("r1", "report", map[string]any{"answer": map[string]any{"employer": "Example Corp"}})),
		anthropictest.Reply("end_turn", anthropictest.Text("reported")),
		anthropictest.Reply("tool_use", anthropictest.ToolUse("f1", "web_fetch", map[string]any{"url": srv.URL + "/b"})),
		anthropictest.Reply("end_turn", anthropictest.Text("done")),
	)
	if ran := runApp(t, "agent", "run", "--thread", fmt.Sprint(thread), "--text", "where does Alice work"); ran.code != cli.ExitOK {
		t.Fatalf("lookup run: %d %s", ran.code, ran.stderr)
	}
	requests := api.Requests()[start:]
	if len(requests) != 6 {
		t.Fatalf("%d model requests, want 6", len(requests))
	}
	result := lookupResult(t, requests[5], "l1")
	answer, _ := result["answer"].(map[string]any)
	if answer["employer"] != "Example Corp" {
		t.Errorf("lookup answer = %v", answer)
	}
	if hits := pages.seen(); !slices.Equal(hits, []string{"/b"}) {
		t.Errorf("page fetches = %v, want /b", hits)
	}
	fetch := toolResultFor(t, requests[5], "f1")
	if fetch["is_error"] == true {
		t.Errorf("web_fetch after a name-only lookup failed: %v", fetch)
	}
}

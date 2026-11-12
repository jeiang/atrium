package main

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/tool"
)

func searchHits(t *testing.T, d *db.DB, threadID int64, toolUseID string) ([]int64, string) {
	t.Helper()
	result := kbResult(t, d, threadID, toolUseID)
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	entries, _ := result["hits"].([]any)
	ids := make([]int64, 0, len(entries))
	for _, entry := range entries {
		hit, _ := entry.(map[string]any)
		id, _ := hit["thread_id"].(float64)
		ids = append(ids, int64(id))
	}
	return ids, string(raw)
}

func TestPrivateProjectSummariesStayOutOfOtherReaders(t *testing.T) {
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	if ran := runApp(t, "agent", "run", "--text", "warm"); ran.code != cli.ExitOK {
		t.Fatalf("warm run: %d %s", ran.code, ran.stderr)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), db.Key{})
	defer func() { _ = d.Close() }()
	setup := []string{
		"INSERT INTO projects (id, name, effort, context_limit, private, created_at, updated_at) VALUES (1, 'Household', 'low', 100000, 1, 0, 0), (2, 'Studio', 'low', 100000, 0, 0, 0)",
		"INSERT INTO threads (id, kind, title, effort, created_at, updated_at, project_id, marks) VALUES " +
			"(2, 'chat', 'Vault notes', 'low', 0, 0, 1, 0), (3, 'chat', 'Studio notes', 'low', 0, 0, 2, 0), " +
			"(4, 'chat', 'Loose notes', 'low', 0, 0, NULL, 0), (5, 'chat', 'Vault sibling', 'low', 0, 0, 1, 2), " +
			"(6, 'chat', 'Vault ghost', 'low', 0, 0, 1, 0), (7, 'chat', 'Vault orphan', 'low', 0, 0, 99, 0)",
		"UPDATE threads SET deleted_at = 1 WHERE id = 6",
		"INSERT INTO memory_nodes (thread_id, level, idx, text, bytes, state, src_from_seq, src_to_seq, created_at) VALUES " +
			"(3, 1, 0, 'the vault combination is 1111', 28, 'summary', 20, 21, 0), " +
			"(4, 1, 0, 'the vault combination is 2222', 28, 'summary', 30, 31, 0), " +
			"(5, 1, 0, 'the vault spare key is 5150', 26, 'summary', 40, 41, 0), " +
			"(6, 1, 0, 'the vault ghost is 6666', 24, 'summary', 50, 51, 0), " +
			"(7, 1, 0, 'the vault orphan is 7777', 24, 'summary', 60, 61, 0)",
	}
	for _, stmt := range setup {
		if _, err := d.Write.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}

	run := func(thread int64, toolUseID string, args map[string]any) {
		t.Helper()
		api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse(toolUseID, "search_threads", args)))
		api.Push(anthropictest.Reply("end_turn", anthropictest.Text("done")))
		if ran := runApp(t, "agent", "run", "--thread", strconv.FormatInt(thread, 10), "--text", "search the vault"); ran.code != cli.ExitOK {
			t.Fatalf("thread %d run: %d %s", thread, ran.code, ran.stderr)
		}
	}

	run(1, "s1", map[string]any{"query": "vault", "limit": 50})
	ids, raw := searchHits(t, d, 1, "s1")
	if !slices.Contains(ids, int64(3)) || !slices.Contains(ids, int64(4)) {
		t.Errorf("outside reader hits = %v, want the studio and loose threads", ids)
	}
	for _, hidden := range []int64{2, 5, 6, 7} {
		if slices.Contains(ids, hidden) {
			t.Errorf("outside reader saw thread %d: %s", hidden, raw)
		}
	}
	for _, leak := range []string{"8842", "5150", "6666", "7777", "Vault notes", "Vault sibling", "Vault ghost", "Vault orphan"} {
		if strings.Contains(raw, leak) {
			t.Errorf("outside reader leaked %q: %s", leak, raw)
		}
	}
	if marks := threadMarkSet(t, dir, 1); marks&int64(tool.PII) != 0 || marks&int64(tool.Messages) == 0 {
		t.Errorf("outside reader marks = %d, want messages without pii", marks)
	}

	run(1, "s2", map[string]any{"query": "vault", "project": 1})
	if ids, raw := searchHits(t, d, 1, "s2"); len(ids) != 0 {
		t.Errorf("explicit foreign project returned %v: %s", ids, raw)
	}

	run(2, "s3", map[string]any{"query": "vault", "limit": 50})
	ids, raw = searchHits(t, d, 2, "s3")
	if !slices.Contains(ids, int64(5)) {
		t.Errorf("same-project reader hits = %v, want the sibling thread: %s", ids, raw)
	}
	if slices.Contains(ids, int64(6)) || slices.Contains(ids, int64(7)) {
		t.Errorf("same-project reader saw a deleted or dangling thread: %s", raw)
	}
	if !strings.Contains(raw, "5150") {
		t.Errorf("same-project reader misses its project's summary: %s", raw)
	}
	if marks := threadMarkSet(t, dir, 2); marks&int64(tool.PII) == 0 {
		t.Errorf("same-project reader marks = %d, want the sibling's pii inherited", marks)
	}

	run(3, "s4", map[string]any{"query": "vault", "limit": 50})
	if ids, raw := searchHits(t, d, 3, "s4"); slices.Contains(ids, int64(2)) || slices.Contains(ids, int64(5)) || strings.Contains(raw, "8842") {
		t.Errorf("studio reader saw the household project: %v %s", ids, raw)
	}
}

func TestPrivateProjectPinnedFactsStayOutOfOtherPrompts(t *testing.T) {
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	if ran := runApp(t, "agent", "run", "--text", "warm"); ran.code != cli.ExitOK {
		t.Fatalf("warm run: %d %s", ran.code, ran.stderr)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), db.Key{})
	defer func() { _ = d.Close() }()
	setup := []string{
		"INSERT INTO projects (id, name, effort, context_limit, private, created_at, updated_at) VALUES (1, 'Household', 'low', 100000, 1, 0, 0), (2, 'Studio', 'low', 100000, 0, 0, 0)",
		"INSERT INTO threads (id, kind, title, effort, created_at, updated_at, project_id) VALUES " +
			"(2, 'chat', 'Household chat', 'low', 0, 0, 1), (3, 'chat', 'Studio chat', 'low', 0, 0, 2), (4, 'chat', 'Loose chat', 'low', 0, 0, NULL)",
		"INSERT INTO kb_records (id, title, body, scope, project_id, private, marks, writer, pinned, confirmed_at, created_at, updated_at) VALUES " +
			"(1, 'Vault code', 'The vault combination is 8842.', 'personal', 1, 0, 0, 'operator', 1, 1, 0, 0), " +
			"(2, 'Spare key', 'The spare key is 5150.', 'personal', NULL, 1, 0, 'operator', 1, 1, 0, 0), " +
			"(3, 'Office code', 'The office code is 1111.', 'personal', 2, 0, 0, 'operator', 1, 1, 0, 0)",
	}
	for _, stmt := range setup {
		if _, err := d.Write.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}

	prompt := func(thread int64) string {
		t.Helper()
		before := len(api.Requests())
		api.Push(anthropictest.Reply("end_turn", anthropictest.Text("ok")))
		if ran := runApp(t, "agent", "run", "--thread", strconv.FormatInt(thread, 10), "--text", "hello"); ran.code != cli.ExitOK {
			t.Fatalf("thread %d run: %d %s", thread, ran.code, ran.stderr)
		}
		requests := api.Requests()
		if len(requests) != before+1 {
			t.Fatalf("thread %d made %d model requests, want 1", thread, len(requests)-before)
		}
		return systemText(t, requests[before])
	}

	outside := prompt(4)
	if !strings.Contains(outside, "The office code is 1111.") {
		t.Errorf("a no-project prompt lacks the public pinned fact: %.600s", outside)
	}
	for _, hidden := range []string{"The vault combination is 8842.", "The spare key is 5150."} {
		if strings.Contains(outside, hidden) {
			t.Errorf("a no-project prompt carries %q: %.600s", hidden, outside)
		}
	}

	inside := prompt(2)
	if !strings.Contains(inside, "The vault combination is 8842.") {
		t.Errorf("the same-project prompt lacks its pinned fact: %.600s", inside)
	}
	if strings.Contains(inside, "The spare key is 5150.") {
		t.Errorf("the same-project prompt carries a private record without a project: %.600s", inside)
	}

	other := prompt(3)
	if strings.Contains(other, "The vault combination is 8842.") {
		t.Errorf("another project's prompt carries the household fact: %.600s", other)
	}

	base, _, stop := startServeStoppable(t, dir)
	defer stop()
	w := asstBrowse(t, base)
	code, body, _ := w.get("/library/knowledge/profile", false)
	if code != http.StatusOK {
		t.Fatalf("profile page = %d: %s", code, body)
	}
	for _, shown := range []string{"Vault code", "Spare key", "Office code", ">private<", "The office code is 1111."} {
		if !strings.Contains(body, shown) {
			t.Errorf("operator profile page lacks %q: %.600s", shown, body)
		}
	}
	for _, hidden := range []string{"The vault combination is 8842.", "The spare key is 5150."} {
		if strings.Contains(body, hidden) {
			t.Errorf("operator profile page shows %q in the model block: %.600s", hidden, body)
		}
	}
}

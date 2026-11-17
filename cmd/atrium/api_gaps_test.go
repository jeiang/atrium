package main

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func undoableWrite(t *testing.T, s *apiSite, token string, got apiReply, status int) {
	t.Helper()
	apiData(t, got, status)
	effects, _ := got.json["effect_ids"].([]any)
	if len(effects) == 0 || got.json["undoable"] != true {
		t.Fatalf("write is not reported undoable: %s", got.body)
	}
	if undo := s.do(http.MethodPost, fmt.Sprintf("/api/v1/undo/write:%d", number(t, effects[len(effects)-1])), token, nil); undo.status != http.StatusOK {
		t.Fatalf("undo of a write reported undoable: %d %s", undo.status, undo.body)
	}
}

func apiBody(t *testing.T, got apiReply, status int) map[string]any {
	t.Helper()
	if data := apiData(t, got, status); data != nil {
		return data
	}
	return got.json
}

func kindHits(data map[string]any) map[string][]map[string]any {
	out := map[string][]map[string]any{}
	groups, _ := data["groups"].([]any)
	for _, g := range groups {
		group := g.(map[string]any)
		items, _ := group["items"].([]any)
		for _, item := range items {
			out[group["type"].(string)] = append(out[group["type"].(string)], item.(map[string]any))
		}
	}
	return out
}

func TestKnowledgeWritesReportUndoable(t *testing.T) {
	t.Parallel()
	s, token, d := localAPISite(t)
	if _, err := d.Write.Exec("INSERT INTO projects (id, name, instructions, effort, context_limit, private, created_at, updated_at) VALUES (7, 'Harbor', '', 'high', 200000, 0, 0, 0)"); err != nil {
		t.Fatal(err)
	}
	created := apiData(t, s.do(http.MethodPost, "/api/v1/knowledge", token, map[string]any{"title": "Boiler code", "body": "The code is 4417.", "scope": "work"}), http.StatusCreated)
	record := fmt.Sprintf("/api/v1/knowledge/%d", number(t, created["id"]))
	undoableWrite(t, s, token, s.do(http.MethodPatch, record, token, map[string]any{"body": "The code is 9001."}), http.StatusOK)
	for _, action := range []string{"confirm", "retract"} {
		undoableWrite(t, s, token, s.do(http.MethodPost, record+"/"+action, token, nil), http.StatusOK)
	}
	apiData(t, s.do(http.MethodPost, record+"/retract", token, nil), http.StatusOK)
	undoableWrite(t, s, token, s.do(http.MethodPost, record+"/restore", token, nil), http.StatusOK)
	apiData(t, s.do(http.MethodPost, record+"/restore", token, nil), http.StatusOK)
	link := s.do(http.MethodPost, record+"/links", token, map[string]any{"target_kind": "space", "target_id": "7", "relation": "part_of"})
	undoableWrite(t, s, token, link, http.StatusCreated)
}

func subjectTitles(t *testing.T, s *apiSite, token, path string) ([]string, string) {
	t.Helper()
	data := apiData(t, s.do(http.MethodGet, path, token, nil), http.StatusOK)
	var titles []string
	for _, item := range apiItems(t, data) {
		if title := item.(map[string]any)["title"].(string); title != "Standing instructions" {
			titles = append(titles, title)
		}
	}
	next, _ := data["next_cursor"].(string)
	return titles, next
}

func TestKnowledgeSubjectsSort(t *testing.T) {
	t.Parallel()
	s, token, d := localAPISite(t)
	for _, row := range []struct {
		title            string
		created, updated int
	}{{"banana", 10, 40}, {"Apple", 30, 10}, {"cherry", 20, 30}, {"apple pie", 40, 20}} {
		if _, err := d.Write.Exec("INSERT INTO kb_records (title, body, scope, writer, created_at, updated_at) VALUES (?, 'body', 'work', 'operator', ?, ?)", row.title, row.created, row.updated); err != nil {
			t.Fatal(err)
		}
	}
	for sort, want := range map[string]string{
		"":        "banana cherry apple pie Apple",
		"changed": "banana cherry apple pie Apple",
		"title":   "Apple apple pie banana cherry",
		"created": "apple pie Apple cherry banana",
	} {
		first, next := subjectTitles(t, s, token, "/api/v1/knowledge/subjects?limit=3&sort="+sort)
		rest, last := subjectTitles(t, s, token, "/api/v1/knowledge/subjects?limit=3&sort="+sort+"&cursor="+url.QueryEscape(next))
		if got := strings.Join(append(first, rest...), " "); got != want || next == "" || last != "" {
			t.Errorf("sort %q: %q, want %q", sort, got, want)
		}
		if sort == "title" {
			expectAPI(t, s.do(http.MethodGet, "/api/v1/knowledge/subjects?limit=3&sort=created&cursor="+url.QueryEscape(next), token, nil), http.StatusBadRequest, "bad_cursor")
		}
	}
	expectField(t, s.do(http.MethodGet, "/api/v1/knowledge/subjects?sort=size", token, nil), "sort")
}

func queueItem(t *testing.T, s *apiSite, token, id string) map[string]any {
	t.Helper()
	for _, item := range apiItems(t, apiData(t, s.do(http.MethodGet, "/api/v1/queue", token, nil), http.StatusOK)) {
		if m := item.(map[string]any); m["id"] == id {
			return m
		}
	}
	return nil
}

func TestQueueLoopItemCarriesLoopID(t *testing.T) {
	t.Parallel()
	s, token, d := localAPISite(t)
	if _, err := d.Write.Exec("INSERT INTO open_loops (rowid, thread_key, account, state, last_message_id, since, counterpart, subject, updated_ts) VALUES (41, 'thread-a', 'work', 'owe_reply', 9, 100, 'ana@example.com', 'Lease renewal', 100)"); err != nil {
		t.Fatal(err)
	}
	item := queueItem(t, s, token, "loop:thread-a")
	if item == nil || item["loop_id"] != float64(41) {
		t.Fatalf("queue loop item: %v", item)
	}
	if got := s.do(http.MethodPost, fmt.Sprintf("/api/v1/mail/loops/%d/dismiss", number(t, item["loop_id"])), token, nil); got.status != http.StatusOK {
		t.Fatalf("dismiss by loop_id: %d %s", got.status, got.body)
	}
	if again := queueItem(t, s, token, "loop:thread-a"); again != nil {
		t.Fatalf("dismissed loop still queued: %v", again)
	}
}

func TestAssistantSubagentRolesRead(t *testing.T) {
	t.Parallel()
	s, token, _ := localAPISite(t)
	items := apiItems(t, apiBody(t, s.do(http.MethodGet, "/api/v1/assistant/subagent-roles", token, nil), http.StatusOK))
	mail := spaceItem(t, items, "name", "mail")
	if len(items) < 2 || mail == nil || mail["effort"] != "medium" || mail["description"] == "" || len(mail["tools"].([]any)) == 0 {
		t.Fatalf("subagent roles: %v", items)
	}
	expectConditional(t, s, token, "/api/v1/assistant/subagent-roles")
	if got := s.do(http.MethodPost, "/api/v1/assistant/subagent-roles", token, nil); got.status != http.StatusMethodNotAllowed && got.status != http.StatusNotFound {
		t.Fatalf("roles accepted a write: %d", got.status)
	}
}

func TestMailSearchLeavesOutDisabledAccounts(t *testing.T) {
	t.Parallel()
	s, token, d := localAPISite(t)
	for _, stmt := range []string{
		"INSERT OR IGNORE INTO accounts (name, kind) VALUES ('icloud', 'imap'), ('retired', 'imap')",
		"INSERT INTO mailboxes (id, account, name, imap_name, role, uidvalidity, highest_uid) VALUES (1, 'icloud', 'INBOX', 'INBOX', 'inbox', 1, 1)",
		"INSERT INTO mailboxes (id, account, name, imap_name, role, uidvalidity, highest_uid) VALUES (2, 'retired', 'INBOX', 'INBOX', 'inbox', 1, 2)",
		"INSERT INTO messages (id, account, mailbox_id, uidvalidity, uid, msgid, digest, subject, from_addr, date_ts, first_seen) VALUES (1, 'icloud', 1, 1, 1, 'kept@test', 'kept', 'Harbor fares', 'pat@example.com', 100, 0)",
		"INSERT INTO messages (id, account, mailbox_id, uidvalidity, uid, msgid, digest, subject, from_addr, date_ts, first_seen) VALUES (2, 'retired', 2, 1, 2, 'gone@test', 'gone', 'Harbor tickets', 'sam@example.com', 200, 0)",
	} {
		if _, err := d.Write.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	accounts := func(path string) string {
		t.Helper()
		var names []string
		for _, item := range apiItems(t, apiBody(t, s.do(http.MethodGet, path, token, nil), http.StatusOK)) {
			names = append(names, item.(map[string]any)["account"].(string))
		}
		return strings.Join(names, ",")
	}
	if got := accounts("/api/v1/mail/messages"); got != "retired,icloud" {
		t.Fatalf("an enabled or unconfigured account's mail is missing: %q", got)
	}
	if got := s.do(http.MethodPatch, "/api/v1/mail/accounts/icloud", token, map[string]any{"enabled": false}); got.status != http.StatusConflict {
		t.Fatalf("deployment account disabled through the API: %d %s", got.status, got.body)
	}
}

func TestCommitmentSnoozeHidesFromQueueAndUndoes(t *testing.T) {
	t.Parallel()
	s, token, d := localAPISite(t)
	if _, err := d.Write.Exec("INSERT INTO tasks (id, title, notes, status, priority, due, created_ts, updated_ts) VALUES (5, 'Pay rent', '', 'open', 'normal', '2020-01-01', 0, 0)"); err != nil {
		t.Fatal(err)
	}
	if queueItem(t, s, token, "task:5") == nil {
		t.Fatal("overdue task not queued")
	}
	until := float64(time.Now().Add(48 * time.Hour).Unix())
	snoozed := s.do(http.MethodPost, "/api/v1/commitments/task:5/snooze", token, map[string]any{"until": until})
	if item := apiData(t, snoozed, http.StatusOK)["item"].(map[string]any); item["snoozed_until"] != until {
		t.Fatalf("snooze: %s", snoozed.body)
	}
	if queueItem(t, s, token, "task:5") != nil {
		t.Fatal("snoozed task still queued")
	}
	if item := apiData(t, s.do(http.MethodGet, "/api/v1/commitments/task:5", token, nil), http.StatusOK)["item"].(map[string]any); item["snoozed_until"] != until {
		t.Fatalf("commitment read lacks snoozed_until: %v", item)
	}
	expectField(t, s.do(http.MethodPost, "/api/v1/commitments/task:5/snooze", token, map[string]any{"until": 1}), "until")
	undoableWrite(t, s, token, snoozed, http.StatusOK)
	if queueItem(t, s, token, "task:5") == nil {
		t.Fatal("undone snooze left the task hidden")
	}
}

func TestSpaceCreateDefaultsAndRenameIfMatch(t *testing.T) {
	t.Parallel()
	s, token, _ := localAPISite(t)
	id := number(t, apiBody(t, s.do(http.MethodPost, "/api/v1/knowledge/spaces", token, map[string]any{"name": "Harbor"}), http.StatusCreated)["id"])
	path := fmt.Sprintf("/api/v1/knowledge/spaces/%d", id)
	read := s.do(http.MethodGet, path, token, nil)
	space := apiBody(t, read, http.StatusOK)["space"].(map[string]any)
	if space["effort"] == "" || number(t, space["context_limit"]) <= 0 {
		t.Fatalf("create without effort kept no default: %v", space)
	}
	updated := fmt.Sprint(space["updated_at"])
	expectAPI(t, s.do(http.MethodPatch, path, token, map[string]any{"name": "Harbour"}, "If-Match", "1"), http.StatusConflict, "stale")
	expectAPI(t, s.do(http.MethodPatch, path, token, map[string]any{"name": "Harbour"}, "If-Match", `"other"`), http.StatusConflict, "stale")
	if got := s.do(http.MethodPatch, path, token, map[string]any{"name": "Harbour"}, "If-Match", read.header.Get("ETag")); got.status != http.StatusOK {
		t.Fatalf("rename with the read ETag: %d %s", got.status, got.body)
	}
	expectAPI(t, s.do(http.MethodPatch, path, token, map[string]any{"name": "Port"}, "If-Match", updated), http.StatusConflict, "stale")
	now := apiBody(t, s.do(http.MethodGet, path, token, nil), http.StatusOK)["space"].(map[string]any)
	if got := s.do(http.MethodPatch, path, token, map[string]any{"name": "Port"}, "If-Match", fmt.Sprint(now["updated_at"])); got.status != http.StatusOK {
		t.Fatalf("rename with updated_at: %d %s", got.status, got.body)
	}
}

func TestSearchAllFindsSpacesAndContacts(t *testing.T) {
	t.Parallel()
	s, token, d, contactsDir := linksSite(t)
	if err := os.MkdirAll(contactsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	vcf := "BEGIN:VCARD\r\nVERSION:3.0\r\nUID:card-rui-7\r\nFN:Rui Harborview\r\nORG:Ferries\r\nEND:VCARD\r\n"
	if err := os.WriteFile(filepath.Join(contactsDir, "rui.vcf"), []byte(vcf), 0o600); err != nil {
		t.Fatal(err)
	}
	asstExec(t, d, "INSERT INTO projects (id, name, instructions, effort, context_limit, created_at, updated_at) VALUES (9002, 'Harborview rebuild', '', 'low', 100000, 5, 5)")
	groups := kindHits(apiBody(t, s.do(http.MethodGet, "/api/v1/search/all?q=harborview", token, nil), http.StatusOK))
	if len(groups["space"]) != 1 || number(t, groups["space"][0]["id"]) != 9002 || groups["space"][0]["path"] != "/knowledge/spaces/9002" {
		t.Fatalf("space hits: %v", groups["space"])
	}
	if len(groups["contact"]) != 1 || groups["contact"][0]["ref"] != "card-rui-7" || groups["contact"][0]["detail"] != "Ferries" {
		t.Fatalf("contact hits: %v", groups["contact"])
	}
	only := kindHits(apiBody(t, s.do(http.MethodGet, "/api/v1/search/all?q=harborview&types=contact", token, nil), http.StatusOK))
	if len(only["space"]) != 0 || len(only["contact"]) != 1 {
		t.Fatalf("types=contact: %v", only)
	}
}

func TestHomeNeedsYouReadsTheQueue(t *testing.T) {
	t.Parallel()
	s, token, d := localAPISite(t)
	if _, err := d.Write.Exec("INSERT INTO tasks (id, title, notes, status, priority, due, created_ts, updated_ts) VALUES (6, 'Renew the passport', '', 'open', 'normal', '2020-01-01', 0, 0)"); err != nil {
		t.Fatal(err)
	}
	queue := apiBody(t, s.do(http.MethodGet, "/api/v1/queue", token, nil), http.StatusOK)
	home := s.do(http.MethodGet, "/api/v1/home", token, nil)
	needs := apiBody(t, home, http.StatusOK)["needs_you"].(map[string]any)
	if number(t, needs["total"]) != number(t, queue["total"]) || number(t, needs["total"]) < 1 {
		t.Fatalf("needs_you %v, queue %v", needs, queue)
	}
	found := false
	for _, item := range needs["items"].([]any) {
		found = found || item.(map[string]any)["id"] == "task:6"
	}
	if !found || len(needs["items"].([]any)) > 3 {
		t.Fatalf("needs_you items: %v", needs["items"])
	}
	if _, err := d.Write.Exec("UPDATE tasks SET status = 'done' WHERE id = 6"); err != nil {
		t.Fatal(err)
	}
	if again := s.do(http.MethodGet, "/api/v1/home", token, nil, "If-None-Match", home.header.Get("ETag")); again.status != http.StatusOK {
		t.Fatalf("home revision ignored the queue: %d", again.status)
	}
}

package main

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/db"
)

func reviewSMTP(t *testing.T, dir string) *smtpFake {
	t.Helper()
	fake := &gmailTokenFake{}
	tokenSrv := httptest.NewServer(http.HandlerFunc(fake.handler))
	t.Cleanup(tokenSrv.Close)
	t.Setenv("ATRIUM_GOOGLE_TOKEN_URL", tokenSrv.URL)
	setEnv(t, "GOOGLE_OAUTH_CLIENT_SECRET", "secret")
	setEnv(t, "GOOGLE_GMAIL_REFRESH_TOKEN", "refresh")
	ca := smtpCert(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	smtp := &smtpFake{}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go smtp.handle(c)
		}
	}()
	t.Setenv("ATRIUM_SMTP_ADDR", ln.Addr().String())
	t.Setenv("ATRIUM_SMTP_CA", ca)
	d, err := db.Connect(filepath.Join(dir, "atrium.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := db.Migrate(context.Background(), d, filepath.Join(dir, "atrium.db")); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		"INSERT INTO settings (key, value) VALUES ('google_client_id', 'client-id')",
		"INSERT INTO mail_accounts (id, label, preset, login, enabled, pinned, auth_method, created_at) VALUES ('oauth', 'OAuth', 'gmail', 'atrium.test@gmail.test', 1, 0, 'xoauth2', 0)",
	} {
		if _, err := d.Write.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	return smtp
}

func approvalIDs(t *testing.T, list any) []string {
	t.Helper()
	items, _ := list.([]any)
	out := make([]string, len(items))
	for i, item := range items {
		out[i] = strconv.FormatInt(number(t, item.(map[string]any)["id"]), 10)
	}
	return out
}

func TestAPIApprovesUndoesAndSendsThroughTheSameGates(t *testing.T) {
	dir, dav := davFixture(t)
	smtp := reviewSMTP(t, dir)
	event := map[string]any{"calendar": "icloud", "title": "Dentist", "start": "2026-11-10T12:00:00", "end": "2026-11-10T13:00:00"}
	approve := proposalID(t, toolResult(t, "calendar_create", event))
	event["title"] = "Haircut"
	deny := proposalID(t, toolResult(t, "calendar_create", event))
	toolResult(t, "task_add", map[string]any{"title": "Call the dentist", "due": "2026-11-09"})
	toolResult(t, "draft_email_create", map[string]any{"account": "oauth", "to": []any{"to@x.test"}, "subject": "Lease renewal", "body": "Happy to renew."})
	if len(dav.Puts) != 0 {
		t.Fatalf("asking wrote to the calendar: %v", dav.Puts)
	}
	s := startAPISite(t, dir, nil)
	owner := s.token("owner")
	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
	defer func() { _ = d.Close() }()

	pending := s.do(http.MethodGet, "/api/v1/pending", owner, nil)
	drafted, _ := pending.json["drafts"].([]any)
	if ids := approvalIDs(t, pending.json["approvals"]); pending.status != http.StatusOK || len(ids) != 2 || !strings.Contains(strings.Join(ids, ","), approve) ||
		!strings.Contains(strings.Join(ids, ","), deny) || len(drafted) != 1 {
		t.Fatalf("pending: %d %s", pending.status, pending.body)
	}
	draft := drafted[0].(map[string]any)
	if draft["subject"] != "Lease renewal" || draft["status"] != "draft" || draft["account"] != "oauth" {
		t.Fatalf("pending draft: %v", draft)
	}

	path := "/api/v1/approvals/" + approve + "/approve"
	done := s.do(http.MethodPost, path, owner, nil, "Idempotency-Key", "approve-1")
	approval, _ := done.json["approval"].(map[string]any)
	if done.status != http.StatusOK || done.json["outcome"] != "applied" || approval["status"] != "applied" {
		t.Fatalf("approve: %d %s", done.status, done.body)
	}
	if len(dav.Puts) != 1 || len(dav.Deleted) != 0 {
		t.Fatalf("approval wrote %d events and deleted %d, want one PUT", len(dav.Puts), len(dav.Deleted))
	}
	replayed := s.do(http.MethodPost, path, owner, nil, "Idempotency-Key", "approve-1")
	if replayed.status != http.StatusOK || replayed.body != done.body || replayed.header.Get("Idempotent-Replayed") != "true" {
		t.Fatalf("approve replay: %d %s", replayed.status, replayed.body)
	}
	expectAPI(t, s.do(http.MethodPost, path, owner, nil), http.StatusConflict, "not_approved")
	if len(dav.Puts) != 1 {
		t.Fatalf("a second approval wrote again: %d puts", len(dav.Puts))
	}
	var source string
	if err := d.Read.QueryRow("SELECT source FROM write_resolutions WHERE proposal_id = ? AND outcome = 'approved'", approve).Scan(&source); err != nil || source != "ui" {
		t.Fatalf("approval resolution source = %q, %v", source, err)
	}

	denied := s.do(http.MethodPost, "/api/v1/approvals/"+deny+"/deny", owner, map[string]any{})
	if denied.status != http.StatusOK || denied.json["approval"].(map[string]any)["status"] != "dismissed" {
		t.Fatalf("deny: %d %s", denied.status, denied.body)
	}
	expectAPI(t, s.do(http.MethodPost, "/api/v1/approvals/"+deny+"/deny", owner, nil), http.StatusConflict, "not_dismissed")
	expectAPI(t, s.do(http.MethodPost, "/api/v1/approvals/"+deny+"/approve", owner, nil), http.StatusConflict, "not_approved")
	expectAPI(t, s.do(http.MethodPost, "/api/v1/approvals/999999/approve", owner, nil), http.StatusNotFound, "not_found")
	if len(dav.Puts) != 1 {
		t.Fatalf("deny wrote to the calendar: %d puts", len(dav.Puts))
	}
	if after := s.do(http.MethodGet, "/api/v1/pending", owner, nil); len(approvalIDs(t, after.json["approvals"])) != 0 {
		t.Fatalf("pending after approve and deny: %s", after.body)
	}

	var taskID, writeID, taskThread int64
	if err := d.Read.QueryRow("SELECT id FROM tasks WHERE title = 'Call the dentist'").Scan(&taskID); err != nil {
		t.Fatal(err)
	}
	if err := d.Read.QueryRow("SELECT id, thread_id FROM agent_writes WHERE tool = 'task_add'").Scan(&writeID, &taskThread); err != nil {
		t.Fatal(err)
	}
	entryID := "write:" + strconv.FormatInt(writeID, 10)
	feed := s.do(http.MethodGet, "/api/v1/undo?thread_id="+strconv.FormatInt(taskThread, 10), owner, nil)
	items, _ := feed.json["items"].([]any)
	if feed.status != http.StatusOK || len(items) != 1 || items[0].(map[string]any)["id"] != entryID || items[0].(map[string]any)["undoable"] != true {
		t.Fatalf("undo feed for the task thread: %d %s", feed.status, feed.body)
	}
	all := s.do(http.MethodGet, "/api/v1/undo?limit=1", owner, nil)
	if next, _ := all.json["next_cursor"].(string); next == "" {
		t.Fatalf("undo feed has one entry per page and more to come: %s", all.body)
	} else if rest := s.do(http.MethodGet, "/api/v1/undo?limit=1&cursor="+next, owner, nil); rest.status != http.StatusOK ||
		len(rest.json["items"].([]any)) != 1 || rest.json["items"].([]any)[0].(map[string]any)["id"] == all.json["items"].([]any)[0].(map[string]any)["id"] {
		t.Fatalf("undo feed page 2: %s", rest.body)
	}
	undone := s.do(http.MethodPost, "/api/v1/undo/"+entryID, owner, nil)
	entry, _ := undone.json["entry"].(map[string]any)
	if undone.status != http.StatusOK || undone.json["undone"] != true || entry["undoable"] != false || entry["undone_at"] == nil {
		t.Fatalf("undo: %d %s", undone.status, undone.body)
	}
	var left int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM tasks WHERE id = ?", taskID).Scan(&left); err != nil || left != 0 {
		t.Fatalf("task_add undo left %d tasks, %v", left, err)
	}
	expectAPI(t, s.do(http.MethodPost, "/api/v1/undo/"+entryID, owner, nil), http.StatusConflict, "not_undone")
	expectAPI(t, s.do(http.MethodPost, "/api/v1/undo/write:999999", owner, nil), http.StatusNotFound, "not_found")

	draftPath := "/api/v1/drafts/" + strconv.FormatInt(number(t, draft["id"]), 10)
	reviewed := number(t, draft["revision"])
	edited := s.do(http.MethodPatch, draftPath, owner, map[string]any{"subject": "Lease renewal (signed)"})
	if edited.status != http.StatusOK || number(t, edited.json["revision"]) != reviewed+1 || edited.json["subject"] != "Lease renewal (signed)" {
		t.Fatalf("patch: %d %s", edited.status, edited.body)
	}
	expectAPI(t, s.do(http.MethodPatch, draftPath, owner, map[string]any{"body": "  "}), http.StatusUnprocessableEntity, "invalid_draft")
	expectAPI(t, s.do(http.MethodPost, draftPath+"/send", owner, map[string]any{}), http.StatusUnprocessableEntity, "invalid")
	expectAPI(t, s.do(http.MethodPost, draftPath+"/send", owner, map[string]any{"revision": reviewed}), http.StatusConflict, "stale")
	if _, data := smtp.seen(); data != "" {
		t.Fatalf("a stale send reached SMTP:\n%s", data)
	}
	sent := s.do(http.MethodPost, draftPath+"/send", owner, map[string]any{"revision": reviewed + 1}, "Idempotency-Key", "send-1")
	if sent.status != http.StatusOK || sent.json["draft"].(map[string]any)["status"] != "sent" || sent.json["filed"] != "server" {
		t.Fatalf("send: %d %s", sent.status, sent.body)
	}
	if _, data := smtp.seen(); !strings.Contains(data, "Subject: Lease renewal (signed)") {
		t.Fatalf("SMTP did not get the draft:\n%s", data)
	}
	if again := s.do(http.MethodPost, draftPath+"/send", owner, map[string]any{"revision": reviewed + 1}, "Idempotency-Key", "send-1"); again.status != http.StatusOK ||
		again.header.Get("Idempotent-Replayed") != "true" {
		t.Fatalf("send replay: %d %s", again.status, again.body)
	}
	expectAPI(t, s.do(http.MethodPost, draftPath+"/send", owner, map[string]any{"revision": reviewed + 2}), http.StatusConflict, "not_sent")
	expectAPI(t, s.do(http.MethodPost, draftPath+"/discard", owner, nil), http.StatusConflict, "not_discarded")
	if got := s.do(http.MethodGet, draftPath, owner, nil); got.status != http.StatusOK || got.json["status"] != "sent" {
		t.Fatalf("draft after send: %d %s", got.status, got.body)
	}
}

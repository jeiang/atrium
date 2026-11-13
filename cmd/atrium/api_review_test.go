package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
)

func reviewSMTP(t *testing.T, dir string) *smtpFake {
	t.Helper()
	fake := &gmailTokenFake{}
	tokenSrv := httptest.NewServer(http.HandlerFunc(fake.handler))
	t.Cleanup(tokenSrv.Close)
	t.Setenv("ATRIUM_GOOGLE_TOKEN_URL", tokenSrv.URL)
	googleHostConfigWith(t, dir, mailConnection("icloud", "icloud", "operator@icloud.invalid", defaultMailPassword, true))
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
	insertMailAccount(t, d, "oauth", "gmail", "atrium.test@gmail.test", "xoauth2")
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
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), db.Key{})
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
	if err := d.Read.QueryRow("SELECT source FROM effect_resolutions WHERE proposal_id = ? AND effect_id IS NULL AND outcome = 'approved'", approve).Scan(&source); err != nil || source != "api" {
		t.Fatalf("approval resolution source = %q, %v", source, err)
	}

	denied := s.do(http.MethodPost, "/api/v1/approvals/"+deny+"/deny", owner, map[string]any{})
	if denied.status != http.StatusOK || denied.json["approval"].(map[string]any)["status"] != "dismissed" {
		t.Fatalf("deny: %d %s", denied.status, denied.body)
	}
	expectAPI(t, s.do(http.MethodPost, "/api/v1/approvals/"+deny+"/deny", owner, nil), http.StatusConflict, "not_dismissed")
	expectAPI(t, s.do(http.MethodPost, "/api/v1/approvals/"+deny+"/approve", owner, nil), http.StatusConflict, "not_approved")
	expectAPI(t, s.do(http.MethodPost, "/api/v1/approvals/999999/approve", owner, nil), http.StatusNotFound, "not_found")
	expectAPI(t, s.do(http.MethodPost, "/api/v1/approvals/999999/approve", owner, map[string]any{"selection": []any{1}}), http.StatusNotFound, "not_found")
	if len(dav.Puts) != 1 {
		t.Fatalf("deny wrote to the calendar: %d puts", len(dav.Puts))
	}
	if after := s.do(http.MethodGet, "/api/v1/pending", owner, nil); len(approvalIDs(t, after.json["approvals"])) != 0 {
		t.Fatalf("pending after approve and deny: %s", after.body)
	}

	var taskID, effectID, taskThread int64
	if err := d.Read.QueryRow("SELECT id FROM tasks WHERE title = 'Call the dentist'").Scan(&taskID); err != nil {
		t.Fatal(err)
	}
	if err := d.Read.QueryRow("SELECT id, thread_id FROM effects WHERE operation = 'task_add' AND direction = 'forward'").Scan(&effectID, &taskThread); err != nil {
		t.Fatal(err)
	}
	entryID := strconv.FormatInt(effectID, 10)
	feed := s.do(http.MethodGet, "/api/v1/effects?thread_id="+strconv.FormatInt(taskThread, 10), owner, nil)
	items, _ := feed.json["items"].([]any)
	if feed.status != http.StatusOK || len(items) != 1 || number(t, items[0].(map[string]any)["id"]) != effectID || items[0].(map[string]any)["undoable"] != true {
		t.Fatalf("effects for the task thread: %d %s", feed.status, feed.body)
	}
	all := s.do(http.MethodGet, "/api/v1/undo?limit=1", owner, nil)
	if next, _ := all.json["next_cursor"].(string); next == "" {
		t.Fatalf("undo feed has one entry per page and more to come: %s", all.body)
	} else if rest := s.do(http.MethodGet, "/api/v1/undo?limit=1&cursor="+next, owner, nil); rest.status != http.StatusOK ||
		len(rest.json["items"].([]any)) != 1 || rest.json["items"].([]any)[0].(map[string]any)["id"] == all.json["items"].([]any)[0].(map[string]any)["id"] {
		t.Fatalf("undo feed page 2: %s", rest.body)
	}
	undone := s.do(http.MethodPost, "/api/v1/effects/"+entryID+"/undo", owner, nil)
	entry, _ := undone.json["effect"].(map[string]any)
	if undone.status != http.StatusOK || undone.json["undone"] != true || entry["undoable"] != false || entry["undone_at"] == nil {
		t.Fatalf("undo: %d %s", undone.status, undone.body)
	}
	var left int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM tasks WHERE id = ?", taskID).Scan(&left); err != nil || left != 0 {
		t.Fatalf("task_add undo left %d tasks, %v", left, err)
	}
	expectAPI(t, s.do(http.MethodPost, "/api/v1/effects/"+entryID+"/undo", owner, nil), http.StatusConflict, "not_undone")
	expectAPI(t, s.do(http.MethodGet, "/api/v1/effects/999999", owner, nil), http.StatusNotFound, "not_found")

	draftPath := "/api/v1/drafts/" + strconv.FormatInt(number(t, draft["id"]), 10)
	reviewed := number(t, draft["revision"])
	edited := s.do(http.MethodPatch, draftPath, owner, map[string]any{"subject": "Lease renewal (signed)", "revision": reviewed})
	if edited.status != http.StatusOK || number(t, edited.json["revision"]) != reviewed+1 || edited.json["subject"] != "Lease renewal (signed)" {
		t.Fatalf("patch: %d %s", edited.status, edited.body)
	}
	expectAPI(t, s.do(http.MethodPatch, draftPath, owner, map[string]any{"subject": "Overwritten", "revision": reviewed}), http.StatusConflict, "stale")
	if current := s.do(http.MethodGet, draftPath, owner, nil); current.status != http.StatusOK || current.json["subject"] != "Lease renewal (signed)" || number(t, current.json["revision"]) != reviewed+1 {
		t.Fatalf("stale edit changed draft: %d %s", current.status, current.body)
	}
	var audits int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM operation_audit WHERE operation='mail.draft-update' AND target=? AND source='api' AND outcome='conflict'", strconv.FormatInt(number(t, draft["id"]), 10)).Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("stale draft audit: %d %v", audits, err)
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

func TestPendingRevisionChangesAfterDecision(t *testing.T) {
	dir, dav := davFixture(t)
	reviewSMTP(t, dir)
	event := map[string]any{"calendar": "icloud", "title": "Dentist", "start": "2026-11-10T12:00:00", "end": "2026-11-10T13:00:00"}
	approve := proposalID(t, toolResult(t, "calendar_create", event))
	toolResult(t, "task_add", map[string]any{"title": "Call the dentist", "due": "2026-11-09"})
	if listed := runApp(t, "effects", "list", "--before", "-1"); listed.code != 0 || !strings.Contains(listed.stdout, "task_add") {
		t.Fatalf("effects list --before -1: %d %s %s", listed.code, listed.stdout, listed.stderr)
	}
	s := startAPISite(t, dir, nil)
	owner := s.token("owner")
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), db.Key{})
	defer func() { _ = d.Close() }()

	first := s.do(http.MethodGet, "/api/v1/pending", owner, nil)
	tag := first.header.Get("ETag")
	if ids := approvalIDs(t, first.json["approvals"]); first.status != http.StatusOK || tag == "" || len(ids) != 1 || ids[0] != approve {
		t.Fatalf("pending: %d etag %q %s", first.status, tag, first.body)
	}
	keys := []int{strings.Index(first.body, `"approvals":`), strings.Index(first.body, `"blocked":`), strings.Index(first.body, `"counts":`), strings.Index(first.body, `"drafts":`)}
	if !slices.IsSorted(keys) || keys[0] < 0 {
		t.Fatalf("pending keys out of order %v: %s", keys, first.body)
	}
	if same := s.do(http.MethodGet, "/api/v1/pending", owner, nil, "If-None-Match", tag); same.status != http.StatusNotModified || same.body != "" || same.header.Get("ETag") != tag {
		t.Fatalf("unchanged pending: %d etag %q %s", same.status, same.header.Get("ETag"), same.body)
	}
	if done := s.do(http.MethodPost, "/api/v1/approvals/"+approve+"/approve", owner, nil); done.status != http.StatusOK || done.json["outcome"] != "applied" {
		t.Fatalf("approve: %d %s", done.status, done.body)
	}
	if len(dav.Puts) != 1 {
		t.Fatalf("approval wrote %d events, want 1", len(dav.Puts))
	}
	stale := s.do(http.MethodGet, "/api/v1/pending", owner, nil, "If-None-Match", tag)
	fresh := stale.header.Get("ETag")
	if stale.status != http.StatusOK || len(approvalIDs(t, stale.json["approvals"])) != 0 || fresh == "" || fresh == tag {
		t.Fatalf("pending after the decision with the old etag: %d etag %q %s", stale.status, fresh, stale.body)
	}
	if current := s.do(http.MethodGet, "/api/v1/pending", owner, nil, "If-None-Match", fresh); current.status != http.StatusNotModified {
		t.Fatalf("pending with the current etag: %d %s", current.status, current.body)
	}

	effects := s.do(http.MethodGet, "/api/v1/effects", owner, nil)
	effectsTag := effects.header.Get("ETag")
	if items, _ := effects.json["items"].([]any); effects.status != http.StatusOK || effectsTag == "" || len(items) == 0 {
		t.Fatalf("effects: %d etag %q %s", effects.status, effectsTag, effects.body)
	}
	if same := s.do(http.MethodGet, "/api/v1/effects", owner, nil, "If-None-Match", effectsTag); same.status != http.StatusNotModified {
		t.Fatalf("unchanged effects: %d %s", same.status, same.body)
	}
	if _, err := d.Write.Exec("UPDATE effects SET lease_at = lease_at + 1 WHERE id = (SELECT MAX(id) FROM effects)"); err != nil {
		t.Fatal(err)
	}
	renewed := s.do(http.MethodGet, "/api/v1/effects", owner, nil, "If-None-Match", effectsTag)
	if renewed.status != http.StatusOK || renewed.header.Get("ETag") == effectsTag {
		t.Fatalf("effects after a lease renewal answered %d with etag %q (was %q)", renewed.status, renewed.header.Get("ETag"), effectsTag)
	}
	undo := s.do(http.MethodGet, "/api/v1/undo", owner, nil)
	if undo.status != http.StatusOK || undo.header.Get("ETag") == "" {
		t.Fatalf("undo feed: %d %s", undo.status, undo.body)
	}
	if same := s.do(http.MethodGet, "/api/v1/undo", owner, nil, "If-None-Match", undo.header.Get("ETag")); same.status != http.StatusNotModified {
		t.Fatalf("unchanged undo feed: %d %s", same.status, same.body)
	}
}

func TestApprovalSourceAndInvalidSelection(t *testing.T) {
	dir, dav := davFixture(t)
	reviewSMTP(t, dir)
	event := map[string]any{"calendar": "icloud", "title": "Dentist", "start": "2026-11-10T12:00:00", "end": "2026-11-10T13:00:00"}
	viaHTML := proposalID(t, toolResult(t, "calendar_create", event))
	event["title"] = "Haircut"
	viaAPI := proposalID(t, toolResult(t, "calendar_create", event))
	effectsOf := func(d *db.DB, id string) int {
		var n int
		if err := d.Read.QueryRow("SELECT COUNT(*) FROM effects WHERE proposal_id = ?", id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), db.Key{})
	defer func() { _ = d.Close() }()

	for _, bad := range []string{"{"} {
		if got := runApp(t, "proposals", "approve", viaAPI, "--selection", bad); got.code != cli.ExitUsage || effectsOf(d, viaAPI) != 0 {
			t.Fatalf("CLI approve with selection %q: exit %d, %d effects: %s", bad, got.code, effectsOf(d, viaAPI), got.stderr)
		}
	}

	base, _, stop := startServeStoppable(t, dir)
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	if code, _, _ := fetch(t, client, http.MethodGet, base+"/chat", nil); code != http.StatusOK {
		t.Fatalf("GET /chat = %d", code)
	}
	pending := regexp.MustCompile(`id="pending-count"[^>]*>(\d*)`)
	badge := func() string {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, base+"/library/knowledge", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("If-None-Match", "*")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		body, err := io.ReadAll(resp.Body)
		found := pending.FindSubmatch(body)
		if err != nil || resp.StatusCode != http.StatusOK || resp.Header.Get("ETag") != "" || found == nil {
			t.Fatalf("revalidated knowledge page = %d etag %q: %v %.300s", resp.StatusCode, resp.Header.Get("ETag"), err, body)
		}
		return string(found[1])
	}
	if got := badge(); got != "2" {
		t.Fatalf("review badge before the approval = %q, want 2", got)
	}
	if code, body := hxFetch(t, client, http.MethodPost, base+"/proposals/"+viaHTML+"/approve", url.Values{"all": {"1"}}); code != http.StatusOK || !strings.Contains(body, `data-status="applied"`) {
		t.Fatalf("HTML approve: %d\n%s", code, body)
	}
	if got := badge(); got != "1" {
		t.Fatalf("review badge after the approval = %q, want 1", got)
	}
	stop()

	s := startAPISite(t, dir, nil)
	owner := s.token("owner")
	reply := s.do(http.MethodPost, "/api/v1/approvals/"+viaAPI+"/approve", owner, map[string]any{"selection": []any{1}})
	expectAPI(t, reply, http.StatusUnprocessableEntity, "bad_selection")
	if fields, _ := reply.json["error"].(map[string]any)["fields"].([]any); len(fields) != 1 || !strings.Contains(reply.body, `"selection"`) || effectsOf(d, viaAPI) != 0 {
		t.Fatalf("API bad selection: %s, %d effects", reply.body, effectsOf(d, viaAPI))
	}
	if done := s.do(http.MethodPost, "/api/v1/approvals/"+viaAPI+"/approve", owner, nil); done.status != http.StatusOK || done.json["outcome"] != "applied" {
		t.Fatalf("API approve: %d %s", done.status, done.body)
	}
	if len(dav.Puts) != 2 {
		t.Fatalf("approvals wrote %d events, want 2", len(dav.Puts))
	}
	for id, source := range map[string]string{viaHTML: "ui", viaAPI: "api"} {
		var resolution, audited string
		if err := d.Read.QueryRow("SELECT source FROM effect_resolutions WHERE proposal_id = ? AND outcome = 'approved'", id).Scan(&resolution); err != nil {
			t.Fatal(err)
		}
		if err := d.Read.QueryRow("SELECT source FROM operation_audit WHERE operation = 'review.approve' AND outcome = 'ok' AND actor = 'operator' AND target = ?", "proposal:"+id).Scan(&audited); err != nil {
			t.Fatal(err)
		}
		if resolution != source || audited != source {
			t.Errorf("proposal %s: resolution source %q, audit source %q, want %q", id, resolution, audited, source)
		}
	}
}

func TestReviewZeroTargetsKeepTheirNamespace(t *testing.T) {
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	base := startServeIn(t, dir)
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
	defer func() { _ = d.Close() }()
	for _, command := range []string{"effects", "writes", "live"} {
		for _, action := range []string{"undo", "resolve"} {
			args := []string{command, action, "0"}
			if action == "resolve" {
				args = append(args, "--outcome", "not-applied")
			}
			ran := runApp(t, args...)
			if ran.code != cli.ExitError {
				t.Fatalf("%v = %d %s", args, ran.code, ran.stderr)
			}
			target := "effect:0"
			if command == "live" {
				target = "mail:0"
			}
			var got, outcome string
			if err := d.Read.QueryRow("SELECT target, outcome FROM operation_audit WHERE operation = ? ORDER BY id DESC LIMIT 1", "review."+action).Scan(&got, &outcome); err != nil || got != target || outcome != "not_found" {
				t.Fatalf("%v audit = %q %q, %v", args, got, outcome, err)
			}
		}
	}
	ran := runApp(t, "effects", "resolve", "0", "--outcome", "not-applied", "--actor", "")
	if ran.code != cli.ExitError || !strings.Contains(ran.stderr, "actor") {
		t.Fatalf("empty actor = %d %s", ran.code, ran.stderr)
	}
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	fetch(t, client, http.MethodGet, base+"/review", nil)
	for _, kind := range []string{"effect", "mail"} {
		code, _, _ := fetch(t, client, http.MethodPost, base+"/activity/"+kind+"/0/undo", url.Values{})
		if code != http.StatusNotFound {
			t.Fatalf("%s zero undo = %d", kind, code)
		}
	}
}

func TestDraftDiscardMissingIsHandled(t *testing.T) {
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	base := startServeIn(t, dir)
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	fetch(t, client, http.MethodGet, base+"/review", nil)
	if code, _, _ := fetch(t, client, http.MethodPost, base+"/drafts/999999/discard", url.Values{}); code != http.StatusConflict {
		t.Fatalf("missing full-page discard = %d", code)
	}
	if code, _ := hxFetch(t, client, http.MethodPost, base+"/drafts/999999/discard", url.Values{}); code != http.StatusNotFound {
		t.Fatalf("missing partial discard = %d", code)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
	defer func() { _ = d.Close() }()
	var count int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM operation_audit WHERE operation='mail.draft-discard' AND target='999999' AND outcome='not_found' AND source='ui'").Scan(&count); err != nil || count != 2 {
		t.Fatalf("missing discard audits: %d %v", count, err)
	}
}

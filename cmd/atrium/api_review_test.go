package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"math"
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
	browser := oidcBrowser(t, s.base, s.idp, "owner")
	htmlPath := s.base + "/drafts/" + strconv.FormatInt(number(t, draft["id"]), 10)
	submitted := url.Values{
		"account": {"oauth"}, "subject": {"Operator's unsaved subject"},
		"body": {"Operator's unsaved body"}, "to": {"operator-edit@x.test"}, "cc": {"copy-edit@x.test"},
		"revision": {strconv.FormatInt(reviewed, 10)},
	}
	for _, partial := range []bool{true, false, true} {
		var code int
		var body string
		if partial {
			code, body = hxFetch(t, browser, http.MethodPost, htmlPath, submitted)
		} else {
			code, body, _ = fetch(t, browser, http.MethodPost, htmlPath, submitted)
		}
		if code != http.StatusConflict {
			t.Fatalf("stale HTML edit (partial %v): %d %s", partial, code, body)
		}
		for _, want := range []string{"Operator&#39;s unsaved subject", "Operator&#39;s unsaved body", "operator-edit@x.test", "copy-edit@x.test", `name="revision" value="` + strconv.FormatInt(reviewed, 10) + `"`} {
			if !strings.Contains(body, want) {
				t.Fatalf("stale HTML edit lost %q (partial %v): %s", want, partial, body)
			}
		}
	}
	if current := s.do(http.MethodGet, draftPath, owner, nil); current.status != http.StatusOK || current.json["subject"] != "Lease renewal (signed)" || current.json["body"] != "Happy to renew." || number(t, current.json["revision"]) != reviewed+1 {
		t.Fatalf("stale HTML edit changed draft: %d %s", current.status, current.body)
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

func TestProposalsAPIReadAndDecide(t *testing.T) {
	dir, dav := davFixture(t)
	event := map[string]any{"calendar": "icloud", "title": "Dentist", "start": "2026-11-10T12:00:00", "end": "2026-11-10T13:00:00"}
	var ids []string
	for _, title := range []string{"Dentist", "Haircut", "Plumber"} {
		event["title"] = title
		ids = append(ids, proposalID(t, toolResult(t, "calendar_create", event)))
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
	t.Cleanup(func() { _ = d.Close() })
	insert := func(query string, args ...any) int64 {
		t.Helper()
		res, err := d.Write.Exec(query, args...)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		return id
	}
	project := insert("INSERT INTO projects (name, instructions, effort, context_limit, private, created_at, updated_at) VALUES ('hidden', '', 'low', 90000, 1, 1, 1)")
	thread := insert("INSERT INTO threads (kind, title, project_id, effort, model, context_limit, created_at, updated_at) VALUES ('chat', 'private', ?, 'low', 'claude-haiku-5-5', 90000, 1, 1)", project)
	insert("INSERT INTO operation_audit (ts, operation, actor, source, thread_id, outcome, target) VALUES (2, 'tasks.add', 'agent', 'tool', ?, 'ok', 'private-marker')", thread)
	insert("INSERT INTO operation_audit (ts, operation, actor, source, outcome, target) VALUES (2, 'tasks.add', 'agent', 'tool', 'ok', 'public-marker')")
	insert("INSERT INTO operation_audit (ts, operation, actor, source, outcome, target) VALUES (2, 'projects.file-upload', 'operator', 'ui', 'ok', ?)", fmt.Sprintf("project:%d", project))
	insert("INSERT INTO operation_audit (ts, operation, actor, source, outcome, target) VALUES (2, 'memory.work', 'job', 'job', 'ok', ?)", fmt.Sprintf("thread:%d", thread))
	seeded := map[string]bool{}
	for i := range 120 {
		ts := 1 + float64(i/7)/100
		seeded[fmt.Sprintf("operation:%d", insert("INSERT INTO operation_audit (ts, operation, actor, source, outcome, target) VALUES (?, 'tasks.add', 'agent', 'tool', 'ok', 'paged')", ts))] = true
		seeded[fmt.Sprintf("settings:%d", insert("INSERT INTO settings_audit (ts, key, old_value, new_value, secret, actor, source) VALUES (?, 'paged', 'a', 'b', 0, 'operator', 'ui')", ts))] = true
		seeded[fmt.Sprintf("phone:%d", insert("INSERT INTO effect_resolutions (ts, actor, source, outcome, prior_error, legacy_json) VALUES (?, 'operator', 'phone', 'done', '', '{}')", ts))] = true
	}
	token := localAPIToken(t, "api")
	base, _, stop, err := startServeAppArgs(t, fixtureFor(t).app, &lockedBuffer{}, "--auth", "local")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	s := &apiSite{t: t, base: base}

	listed := apiItems(t, apiData(t, s.do(http.MethodGet, "/api/v1/proposals?status=pending", token, nil), http.StatusOK))
	if len(listed) != 3 {
		t.Fatalf("pending proposals: %v", listed)
	}
	for _, it := range listed {
		p := it.(map[string]any)
		if p["kind"] != "calendar_event" || !slices.Equal(anyStrings(p["actions"]), []string{"approve", "deny"}) {
			t.Fatalf("proposal summary: %v", p)
		}
	}
	first := apiData(t, s.do(http.MethodGet, "/api/v1/proposals?status=pending&limit=2", token, nil), http.StatusOK)
	cursor, _ := first["next_cursor"].(string)
	if len(apiItems(t, first)) != 2 || cursor == "" {
		t.Fatalf("first page: %v", first)
	}
	if rest := apiData(t, s.do(http.MethodGet, "/api/v1/proposals?status=pending&limit=2&cursor="+cursor, token, nil), http.StatusOK); len(apiItems(t, rest)) != 1 || rest["next_cursor"] != nil {
		t.Fatalf("second page: %v", rest)
	}
	expectAPI(t, s.do(http.MethodGet, "/api/v1/proposals?status=applied&limit=2&cursor="+cursor, token, nil), http.StatusBadRequest, "bad_cursor")
	bad := s.do(http.MethodGet, "/api/v1/proposals?status=bogus", token, nil)
	if expectAPI(t, bad, http.StatusUnprocessableEntity, "invalid"); !strings.Contains(bad.body, `"status"`) {
		t.Fatalf("bad status: %s", bad.body)
	}

	approve := ids[0]
	detail := "/api/v1/proposals/" + approve
	tag := expectConditional(t, s, token, detail)
	shown := apiData(t, s.do(http.MethodGet, detail, token, nil), http.StatusOK)
	payload, _ := shown["payload"].(map[string]any)
	if payload["kind"] != "action" || payload["proposal_kind"] != "calendar_event" || shown["status"] != "pending" {
		t.Fatalf("proposal detail: %v", shown)
	}
	expectAPI(t, s.do(http.MethodGet, "/api/v1/proposals/999999", token, nil), http.StatusNotFound, "not_found")

	path := "/api/v1/approvals/" + approve + "/approve"
	done := s.do(http.MethodPost, path, token, nil, "Idempotency-Key", "decide-1")
	command := done.header.Get("Atrium-Command-Id")
	if done.status != http.StatusOK || done.json["outcome"] != "applied" || command == "" {
		t.Fatalf("approve: %d %v %s", done.status, done.header, done.body)
	}
	again := s.do(http.MethodPost, path, token, nil, "Idempotency-Key", "decide-1")
	if again.status != http.StatusOK || again.header.Get("Idempotent-Replayed") != "true" || len(dav.Puts) != 1 {
		t.Fatalf("approve replay: %d %s, %d puts", again.status, again.body, len(dav.Puts))
	}
	expectAPI(t, s.do(http.MethodPost, path, token, nil), http.StatusConflict, "not_approved")
	if len(dav.Puts) != 1 {
		t.Fatalf("a repeated approval wrote again: %d puts", len(dav.Puts))
	}
	if _, err := d.Write.Exec("UPDATE effects SET state = 'unknown' WHERE proposal_id = ?", approve); err != nil {
		t.Fatal(err)
	}
	blockedDetail := anyStrings(apiData(t, s.do(http.MethodGet, detail, token, nil), http.StatusOK)["actions"])
	var blockedListed []string
	for _, it := range apiItems(t, apiData(t, s.do(http.MethodGet, "/api/v1/proposals?status=applied", token, nil), http.StatusOK)) {
		if p := it.(map[string]any); strconv.FormatInt(number(t, p["id"]), 10) == approve {
			blockedListed = anyStrings(p["actions"])
		}
	}
	if !slices.Equal(blockedListed, blockedDetail) || !slices.Contains(blockedDetail, "resolve") {
		t.Fatalf("blocked proposal actions: list %v, detail %v", blockedListed, blockedDetail)
	}
	if _, err := d.Write.Exec("UPDATE effects SET state = 'done' WHERE proposal_id = ?", approve); err != nil {
		t.Fatal(err)
	}
	if after := expectChanged(t, s, token, detail, tag); after["status"] != "applied" {
		t.Fatalf("approved detail: %v", after)
	}
	var executions int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM operation_audit WHERE operation = 'review.approve' AND target = ?", "proposal:"+approve).Scan(&executions); err != nil || executions != 2 {
		t.Fatalf("approve audits = %d, %v; want the approval and the refused retry", executions, err)
	}

	activity := s.do(http.MethodGet, "/api/v1/activity", token, nil)
	items := apiItems(t, apiData(t, activity, http.StatusOK))
	var audited map[string]any
	for _, it := range items {
		if e := it.(map[string]any); e["source"] == "operation" && e["command_id"] == command {
			audited = e
		}
	}
	if audited == nil || audited["operation"] != "review.approve" || audited["outcome"] != "ok" || audited["target"] != "proposal:"+approve {
		t.Fatalf("no operation audit for command %s: %.2000s", command, activity.body)
	}
	effects := anyNumbers(t, audited["effect_ids"])
	if len(effects) == 0 {
		t.Fatalf("approval audit names no effect: %v", audited)
	}
	var effect map[string]any
	for _, it := range items {
		if e := it.(map[string]any); e["source"] == "effect" && number(t, e["id"]) == effects[0] {
			effect = e
		}
	}
	if effect == nil || effect["outcome"] == "" || effect["effective_outcome"] == "" {
		t.Fatalf("approved effect missing from activity: %.2000s", activity.body)
	}
	if strings.Contains(activity.body, "private-marker") || !strings.Contains(activity.body, "public-marker") ||
		strings.Contains(activity.body, fmt.Sprintf(`"project:%d"`, project)) || strings.Contains(activity.body, fmt.Sprintf(`"thread:%d"`, thread)) {
		t.Fatalf("activity ignores private-project isolation: %.2000s", activity.body)
	}
	for _, leaked := range []string{`"before"`, `"after"`, `"body"`, `"prompt"`, `"details_json"`} {
		if strings.Contains(activity.body, leaked) {
			t.Fatalf("activity carries %s: %.2000s", leaked, activity.body)
		}
	}

	rules := func() map[string]map[string]any {
		t.Helper()
		got := apiData(t, s.do(http.MethodGet, "/api/v1/permissions", token, nil), http.StatusOK)
		out := map[string]map[string]any{}
		for _, r := range got["rules"].([]any) {
			out[r.(map[string]any)["key"].(string)] = r.(map[string]any)
		}
		if number(t, got["pending_requests"]) != 0 || len(out) != 24 {
			t.Fatalf("permissions: %v", got)
		}
		return out
	}
	const floored, open = "permission_internet_untrusted_knowledge", "permission_internet_clean"
	if r := rules()[floored]; r["floor"] != "ask" || !slices.Equal(anyStrings(r["choices"]), []string{"ask", "deny"}) {
		t.Fatalf("floored rule: %v", r)
	}
	permTag := expectConditional(t, s, token, "/api/v1/permissions")
	widen := s.do(http.MethodPut, "/api/v1/permissions", token, map[string]any{"changes": []any{map[string]any{"key": floored, "value": "allow"}}})
	if expectAPI(t, widen, http.StatusUnprocessableEntity, "invalid"); !strings.Contains(widen.body, `"fields"`) {
		t.Fatalf("below floor: %s", widen.body)
	}
	expectAPI(t, s.do(http.MethodPut, "/api/v1/permissions", token, map[string]any{"changes": []any{map[string]any{"key": "tool_budget", "value": "allow"}}}), http.StatusUnprocessableEntity, "invalid")
	if r := rules()[floored]; r["effective"] != "ask" {
		t.Fatalf("refused widening changed the rule: %v", r)
	}
	all := rules()
	for i := range 5 {
		var changes []any
		for key := range all {
			changes = append(changes, map[string]any{"key": key, "value": []string{"deny", "ask"}[i%2]})
		}
		if got := apiData(t, s.do(http.MethodPut, "/api/v1/permissions", token, map[string]any{"changes": changes}), http.StatusOK); len(got["keys"].([]any)) != len(all) {
			t.Fatalf("permissions update: %v", got)
		}
	}
	expectChanged(t, s, token, "/api/v1/permissions", permTag)
	if r := rules()[open]; r["value"] != "deny" || r["effective"] != "deny" || r["source"] != "instance" {
		t.Fatalf("updated rule: %v", r)
	}

	seen := map[string]bool{}
	page := apiData(t, s.do(http.MethodGet, "/api/v1/activity?limit=100", token, nil), http.StatusOK)
	next, _ := page["next_cursor"].(string)
	if len(apiItems(t, page)) != 100 || next == "" {
		t.Fatalf("activity first page: %d items, cursor %q", len(apiItems(t, page)), next)
	}
	prev := []any{math.Inf(1), int64(math.MaxInt64), "~"}
	for {
		for _, it := range apiItems(t, page) {
			e := it.(map[string]any)
			key := e["source"].(string) + ":" + strconv.FormatInt(number(t, e["id"]), 10)
			if seen[key] {
				t.Fatalf("activity repeated %s", key)
			}
			seen[key] = true
			ts, id, source := e["ts"].(float64), number(t, e["id"]), e["source"].(string)
			if pts, pid, psource := prev[0].(float64), prev[1].(int64), prev[2].(string); ts > pts || ts == pts && (id > pid || id == pid && source >= psource) {
				t.Fatalf("activity out of order: %s after %v", key, prev)
			}
			prev = []any{ts, id, source}
		}
		if next == "" {
			break
		}
		expectAPI(t, s.do(http.MethodGet, "/api/v1/activity?category=settings&limit=100&cursor="+next, token, nil), http.StatusBadRequest, "bad_cursor")
		page = apiData(t, s.do(http.MethodGet, "/api/v1/activity?limit=100&cursor="+next, token, nil), http.StatusOK)
		next, _ = page["next_cursor"].(string)
	}
	if len(seen) < 110 {
		t.Fatalf("activity paged %d entries, want the permission changes too", len(seen))
	}
	for key := range seeded {
		if !seen[key] {
			t.Fatalf("activity paging skipped %s", key)
		}
	}
	for _, forged := range []string{"bm90LWpzb24", base64.RawURLEncoding.EncodeToString([]byte(`{"v":1,"op":"review.activity","filter":"x","last":{"t":1,"i":1,"s":"mail"}}`))} {
		expectAPI(t, s.do(http.MethodGet, "/api/v1/activity?cursor="+forged, token, nil), http.StatusBadRequest, "bad_cursor")
	}
	sum := sha256.Sum256(nil)
	for _, last := range []string{`{"o":1000000000,"t":0,"s":"","i":0}`, `{"t":1,"i":0,"s":"mail"}`, `{"t":1,"i":1,"s":"nowhere"}`} {
		forged := base64.RawURLEncoding.EncodeToString([]byte(`{"v":1,"op":"review.activity","filter":"` + hex.EncodeToString(sum[:]) + `","last":` + last + `}`))
		expectAPI(t, s.do(http.MethodGet, "/api/v1/activity?cursor="+forged, token, nil), http.StatusBadRequest, "bad_cursor")
	}
	expectAPI(t, s.do(http.MethodGet, "/api/v1/activity?category=bogus", token, nil), http.StatusUnprocessableEntity, "invalid")

	queued := s.do(http.MethodGet, "/api/v1/review/items", token, nil)
	groups, _ := apiData(t, queued, http.StatusOK)["groups"].([]any)
	found := false
	for _, g := range groups {
		for _, it := range g.(map[string]any)["items"].([]any) {
			if item := it.(map[string]any); item["source_id"] == ids[1] && g.(map[string]any)["group"] == "needs_decision" {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("pending proposal %s not queued for a decision: %.2000s", ids[1], queued.body)
	}
}

func anyStrings(v any) []string {
	items, _ := v.([]any)
	out := make([]string, len(items))
	for i, it := range items {
		out[i], _ = it.(string)
	}
	return out
}

func anyNumbers(t *testing.T, v any) []int64 {
	t.Helper()
	items, _ := v.([]any)
	out := make([]int64, len(items))
	for i, it := range items {
		out[i] = number(t, it)
	}
	return out
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

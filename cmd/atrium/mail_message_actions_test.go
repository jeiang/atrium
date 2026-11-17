package main

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/apps/mail"
	"github.com/jeiang/atrium/internal/apps/mail/mailtest"
)

type actionSite struct {
	m      *mailSafety
	s      *apiSite
	token  string
	base   string
	client *http.Client
}

func actionFixture(t *testing.T, mode, preset string, fake *mailtest.FakeClient) *actionSite {
	t.Helper()
	m := mailEffectsFixture(t, mode, preset, fake)
	m.run(t, "sync")
	token := localAPIToken(t, "api")
	base, _, stop, err := startServeAppArgs(t, m.app, &lockedBuffer{}, "--auth", "local")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	return &actionSite{m: m, s: &apiSite{t: t, base: base}, token: token, base: base, client: mrvClient(t)}
}

func (a *actionSite) post(path string, body any) apiReply {
	return a.s.do(http.MethodPost, "/api/v1"+path, a.token, body)
}

func (a *actionSite) action(id int64, name string, body any) apiReply {
	return a.post(fmt.Sprintf("/mail/messages/%d/%s", id, name), body)
}

func (a *actionSite) lastEffect(t *testing.T, operation string) string {
	t.Helper()
	return a.m.scalar(t, "SELECT CAST(id AS TEXT) FROM effects WHERE origin = 'operator_mail' AND operation = ? ORDER BY id DESC LIMIT 1", operation)
}

func (a *actionSite) undo(t *testing.T, effect string) {
	t.Helper()
	if got := a.post("/undo/write:"+effect, nil); got.status != http.StatusOK {
		t.Fatalf("undo %s: %d %.400s", effect, got.status, got.body)
	}
}

func standardInbox(archive bool) (*mailtest.FakeClient, *mailtest.FakeBox) {
	fake := mailtest.NewFakeClient()
	inbox := fake.AddBox("INBOX", mailtest.NewBox(1))
	if archive {
		fake.AddBox("Archive", mailtest.NewBox(2), `\Archive`)
	}
	fake.AddBox("Sent Messages", mailtest.NewBox(5), `\Sent`)
	fake.AddBox("Deleted Messages", mailtest.NewBox(9), `\Trash`)
	return fake, inbox
}

func dinner() ([]byte, []byte) {
	h := "From: Pat <pat@x.test>\r\nReply-To: replies@x.test\r\nTo: me@x.test\r\nSubject: Dinner\r\nMessage-ID: <dinner@x.test>\r\n" +
		"Date: Tue, 06 Oct 2026 10:00:00 +0000\r\nContent-Type: text/plain\r\n\r\n"
	return []byte(h), []byte("See you at 7.\nBring wine.")
}

func flagsOf(fake *mailtest.FakeClient, box string, uid uint32) []string {
	return fake.Boxes[box].Box.Messages[uid].Flags
}

func topLevel(t *testing.T, got apiReply) map[string]any {
	t.Helper()
	if got.status != http.StatusOK {
		t.Fatalf("status %d, want 200: %.600s", got.status, got.body)
	}
	return got.json
}

func TestMailMessageActionsReadAndFlagWriteRecordAndUndo(t *testing.T) {
	t.Parallel()
	fake, inbox := standardInbox(true)
	h, x := dinner()
	uid := inbox.Add(h, x, []string{`\Seen`}, nil)
	a := actionFixture(t, "live", "icloud", fake)
	id := a.m.messageID(t, "Dinner")

	a.m.fake.FailWrite = func(w mailtest.FakeWrite) error {
		if w.Verb == "STORE" && a.m.scalar(t, "SELECT state FROM effects WHERE origin = 'operator_mail' ORDER BY id DESC LIMIT 1") != "intent" {
			return errors.New("STORE ran before its intent was recorded")
		}
		return nil
	}
	if got := a.action(id, "read", map[string]any{"read": false}); got.status != http.StatusOK {
		t.Fatalf("mark unread: %d %.400s", got.status, got.body)
	}
	a.m.fake.FailWrite = nil
	if slices.Contains(flagsOf(fake, "INBOX", uid), `\Seen`) {
		t.Fatalf("server flags after mark unread = %v", flagsOf(fake, "INBOX", uid))
	}
	if seen := a.m.scalar(t, "SELECT CAST(seen AS TEXT) FROM messages WHERE id = ?", id); seen != "0" {
		t.Fatalf("local seen after mark unread = %s", seen)
	}
	read := a.lastEffect(t, "mail_flag")
	if state := a.m.scalar(t, "SELECT state FROM effects WHERE id = ?", read); state != "done" {
		t.Fatalf("mark unread effect is %s, want done", state)
	}
	a.undo(t, read)
	if !slices.Contains(flagsOf(fake, "INBOX", uid), `\Seen`) {
		t.Fatalf("server flags after undoing mark unread = %v", flagsOf(fake, "INBOX", uid))
	}
	if seen := a.m.scalar(t, "SELECT CAST(seen AS TEXT) FROM messages WHERE id = ?", id); seen != "1" {
		t.Fatalf("local seen after undo = %s", seen)
	}

	if got := a.action(id, "flag", map[string]any{"flagged": true}); got.status != http.StatusOK {
		t.Fatalf("flag: %d %.400s", got.status, got.body)
	}
	if !slices.Contains(flagsOf(fake, "INBOX", uid), `\Flagged`) {
		t.Fatalf("server flags after flag = %v", flagsOf(fake, "INBOX", uid))
	}
	flag := a.lastEffect(t, "mail_flag")
	if flag == read {
		t.Fatalf("flag recorded no new effect")
	}
	a.undo(t, flag)
	if slices.Contains(flagsOf(fake, "INBOX", uid), `\Flagged`) {
		t.Fatalf("server flags after undoing flag = %v", flagsOf(fake, "INBOX", uid))
	}
	if flagged := a.m.scalar(t, "SELECT CAST(flagged AS TEXT) FROM messages WHERE id = ?", id); flagged != "0" {
		t.Fatalf("local flagged after undo = %s", flagged)
	}
}

func TestMailMessageArchiveCopiesDeletesExpungesAndUndoes(t *testing.T) {
	t.Parallel()
	fake, inbox := standardInbox(true)
	h, x := dinner()
	inbox.Add(h, x, []string{`\Seen`}, nil)
	a := actionFixture(t, "live", "icloud", fake)
	id := a.m.messageID(t, "Dinner")
	before := len(fake.Writes)
	got := a.action(id, "archive", nil)
	if data := apiData(t, got, http.StatusOK); data["folder"] != "Archive" {
		t.Fatalf("archive result = %v", data)
	}
	verbs := a.m.verbs()[before:]
	if !slices.Contains(verbs, "COPY") || !slices.Contains(verbs, "STORE") || !slices.Contains(verbs, "EXPUNGE") || slices.Contains(verbs, "MOVE") {
		t.Fatalf("archive verbs = %v, want COPY, STORE and EXPUNGE without MOVE", verbs)
	}
	if n := len(fake.Boxes["Archive"].Box.Messages); n != 1 {
		t.Fatalf("archive holds %d messages, want 1", n)
	}
	if n := len(fake.Boxes["INBOX"].Box.Messages); n != 0 {
		t.Fatalf("inbox holds %d messages after archive, want 0", n)
	}
	a.undo(t, a.lastEffect(t, "mail_archive"))
	if n := len(fake.Boxes["INBOX"].Box.Messages); n != 1 {
		t.Fatalf("inbox holds %d messages after undo, want 1", n)
	}
	if n := len(fake.Boxes["Archive"].Box.Messages); n != 0 {
		t.Fatalf("archive holds %d messages after undo, want 0", n)
	}
}

func TestMailMessageArchiveOnGmailRemovesTheInboxLabelAndUndoes(t *testing.T) {
	t.Parallel()
	fake := mailtest.NewFakeClient()
	fake.Gmail = true
	all := fake.AddBox("[Gmail]/All Mail", mailtest.NewBox(9), `\All`)
	fake.AddBox("[Gmail]/Trash", mailtest.NewBox(10), `\Trash`)
	h, x := dinner()
	uid := all.Add(h, x, []string{`\Seen`}, []string{`\Inbox`})
	a := actionFixture(t, "live", "gmail", fake)
	id := a.m.messageID(t, "Dinner")
	if got := a.action(id, "archive", nil); got.status != http.StatusOK {
		t.Fatalf("gmail archive: %d %.400s", got.status, got.body)
	}
	if labels := fake.Boxes["[Gmail]/All Mail"].Box.Messages[uid].Labels; slices.Contains(labels, `\Inbox`) {
		t.Fatalf("labels after archive = %v", labels)
	}
	a.undo(t, a.lastEffect(t, "mail_archive"))
	if labels := fake.Boxes["[Gmail]/All Mail"].Box.Messages[uid].Labels; !slices.Contains(labels, `\Inbox`) {
		t.Fatalf("labels after undo = %v", labels)
	}
}

func TestMailMessageActionsRefuseInShadowModeButRunWhilePaused(t *testing.T) {
	t.Parallel()
	fake, inbox := standardInbox(true)
	h, x := dinner()
	inbox.Add(h, x, []string{`\Seen`}, nil)
	a := actionFixture(t, "shadow", "icloud", fake)
	id := a.m.messageID(t, "Dinner")
	for _, call := range []struct {
		name string
		body any
	}{{"read", map[string]any{"read": true}}, {"flag", map[string]any{"flagged": true}}, {"archive", nil}} {
		got := a.action(id, call.name, call.body)
		expectAPI(t, got, http.StatusConflict, "shadow_mode")
	}
	if n := a.m.count(t, "SELECT COUNT(*) FROM effects WHERE origin = 'operator_mail'"); n != 0 {
		t.Fatalf("shadow mode recorded %d operator effects", n)
	}
	if len(fake.Writes) != 0 {
		t.Fatalf("shadow mode wrote %v", a.m.verbs())
	}
	if set := runSettings(t, "set", mail.KeyMode, "live"); set.code != 0 {
		t.Fatalf("set mail mode: %d %s", set.code, set.stderr)
	}
	a.m.run(t, "live", "pause")
	if got := a.action(id, "flag", map[string]any{"flagged": true}); got.status != http.StatusOK {
		t.Fatalf("operator flag while paused: %d %.400s", got.status, got.body)
	}
	if !slices.Contains(a.m.verbs(), "STORE") {
		t.Fatalf("operator flag while paused wrote %v", a.m.verbs())
	}
}

func TestMailMessageArchiveRefusesWithoutAnArchiveFolderOrFromAProtectedFolder(t *testing.T) {
	t.Parallel()
	fake, inbox := standardInbox(false)
	h, x := dinner()
	inbox.Add(h, x, []string{`\Seen`}, nil)
	fake.AddBox("Receipts", mailtest.NewBox(4))
	ht, xt := mailtest.RawWith("Old receipt", "shop@x.test", "receipt", "Thanks.")
	fake.Boxes["Receipts"].Box.Add(ht, xt, []string{`\Seen`}, nil)
	a := actionFixture(t, "live", "icloud", fake)
	got := a.action(a.m.messageID(t, "Dinner"), "archive", nil)
	expectAPI(t, got, http.StatusConflict, "no_archive_folder")
	fake.AddBox("Archive", mailtest.NewBox(2), `\Archive`)
	fake.Boxes["Receipts"].Flags = []string{`\Junk`}
	got = a.action(a.m.messageID(t, "Old receipt"), "archive", nil)
	expectAPI(t, got, http.StatusConflict, "protected_folder")
	if n := a.m.count(t, "SELECT COUNT(*) FROM effects WHERE origin = 'operator_mail'"); n != 0 {
		t.Fatalf("refused archives recorded %d effects", n)
	}
	if slices.ContainsFunc(a.m.verbs(), func(v string) bool { return v == "COPY" || v == "MOVE" }) {
		t.Fatalf("refused archives wrote %v", a.m.verbs())
	}
}

func TestMailMessageActionUnknownOutcomeBlocksTheNextWriteWithoutRetry(t *testing.T) {
	t.Parallel()
	fake, inbox := standardInbox(true)
	h, x := dinner()
	inbox.Add(h, x, []string{`\Seen`}, nil)
	a := actionFixture(t, "live", "icloud", fake)
	id := a.m.messageID(t, "Dinner")
	fake.FailWrite = func(w mailtest.FakeWrite) error {
		if w.Verb == "STORE" {
			return &mail.WriteInterrupted{Command: "UID STORE", Err: errors.New("connection reset by peer")}
		}
		return nil
	}
	got := a.action(id, "read", map[string]any{"read": true})
	expectAPI(t, got, http.StatusConflict, "write_outcome_unknown")
	if state := a.m.scalar(t, "SELECT state FROM effects WHERE origin = 'operator_mail'"); state != "unknown" {
		t.Fatalf("interrupted store is %s, want unknown", state)
	}
	fake.FailWrite = nil
	writes := len(fake.Writes)
	got = a.action(id, "flag", map[string]any{"flagged": true})
	expectAPI(t, got, http.StatusConflict, "write_blocked")
	if len(fake.Writes) != writes {
		t.Fatalf("a blocked write reached the server: %v", a.m.verbs()[writes:])
	}
	if n := a.m.count(t, "SELECT COUNT(*) FROM effects WHERE origin = 'operator_mail'"); n != 1 {
		t.Fatalf("%d operator effects, want only the unknown one", n)
	}
}

func TestMailReplyForwardAndOperatorDrafts(t *testing.T) {
	fake, inbox := standardInbox(true)
	h, x := dinner()
	inbox.Add(h, x, []string{`\Seen`}, nil)
	smtp := mrvSMTPServer(t)
	a := actionFixture(t, "live", "icloud", fake)
	id := a.m.messageID(t, "Dinner")

	reply := topLevel(t, a.action(id, "reply", nil))
	if reply["to"] != "replies@x.test" || reply["subject"] != "Re: Dinner" || !strings.Contains(fmt.Sprint(reply["body"]), "> See you at 7.\n> Bring wine.") {
		t.Fatalf("reply draft = %v", reply)
	}
	if fmt.Sprint(reply["in_reply_to_message_id"]) != strconv.FormatInt(id, 10) || reply["thread_id"] != nil {
		t.Fatalf("reply draft links = %v / %v", reply["in_reply_to_message_id"], reply["thread_id"])
	}
	replyID := fmt.Sprint(reply["id"])
	if got := a.m.scalar(t, "SELECT in_reply_to FROM drafts WHERE id = ?", replyID); got != "<dinner@x.test>" {
		t.Fatalf("reply in_reply_to = %q", got)
	}

	forward := topLevel(t, a.action(id, "forward", map[string]any{"to": "friend@x.test"}))
	if forward["to"] != "friend@x.test" || forward["subject"] != "Fwd: Dinner" || !strings.Contains(fmt.Sprint(forward["body"]), "---------- Forwarded message ---------") || !strings.Contains(fmt.Sprint(forward["body"]), "See you at 7.") {
		t.Fatalf("forward draft = %v", forward)
	}
	if forward["in_reply_to_message_id"] != nil {
		t.Fatalf("forward draft replies to %v", forward["in_reply_to_message_id"])
	}
	expectAPI(t, a.action(id, "forward", map[string]any{"to": ""}), http.StatusUnprocessableEntity, "invalid_draft")

	effects := a.m.count(t, "SELECT COUNT(*) FROM effects")
	created := topLevel(t, a.post("/drafts", map[string]any{"account": "work", "to": "sam@x.test", "subject": "Hello", "body": "Checking in."}))
	if created["to"] != "sam@x.test" || created["status"] != "draft" || created["thread_id"] != nil {
		t.Fatalf("operator draft = %v", created)
	}
	if n := a.m.count(t, "SELECT COUNT(*) FROM effects"); n != effects {
		t.Fatalf("an operator draft recorded %d effects", n-effects)
	}
	expectAPI(t, a.post("/drafts", map[string]any{"account": "work", "to": "sam@x.test", "subject": "Empty", "body": ""}), http.StatusUnprocessableEntity, "invalid_draft")

	code, _, _ := mrvDo(t, a.client, http.MethodGet, a.base+"/drafts/"+replyID, nil)
	if code != http.StatusOK {
		t.Fatalf("draft page = %d", code)
	}
	code, body, _ := mrvDo(t, a.client, http.MethodPost, a.base+"/drafts/"+replyID+"/send", url.Values{"revision": {fmt.Sprint(reply["revision"])}}, "HX-Request", "true")
	if code != http.StatusOK {
		t.Fatalf("send reply = %d: %.400s", code, body)
	}
	sent := smtp.sent()
	if len(sent) != 1 || !strings.Contains(sent[0], "In-Reply-To: <dinner@x.test>") || !strings.Contains(sent[0], "replies@x.test") {
		t.Fatalf("sent reply = %q", sent)
	}
}

func TestMailMessagePageActionsAndLoopsLink(t *testing.T) {
	t.Parallel()
	fake, inbox := standardInbox(true)
	h, x := dinner()
	uid := inbox.Add(h, x, []string{`\Seen`}, nil)
	a := actionFixture(t, "live", "icloud", fake)
	id := a.m.messageID(t, "Dinner")
	page := fmt.Sprintf("%s/messages/%d", a.base, id)
	code, body, _ := mrvDo(t, a.client, http.MethodGet, page, nil)
	for _, want := range []string{"Mark unread", ">Flag<", "Archive", "Reply", "Forward"} {
		if code != http.StatusOK || !strings.Contains(body, want) {
			t.Fatalf("message page = %d without %q", code, want)
		}
	}
	code, _, header := mrvDo(t, a.client, http.MethodPost, page+"/flag", url.Values{"on": {"1"}})
	if code != http.StatusSeeOther || header.Get("Location") != fmt.Sprintf("/messages/%d", id) || !slices.Contains(flagsOf(fake, "INBOX", uid), `\Flagged`) {
		t.Fatalf("page flag = %d %q, flags %v", code, header.Get("Location"), flagsOf(fake, "INBOX", uid))
	}
	code, _, header = mrvDo(t, a.client, http.MethodPost, page+"/reply", url.Values{})
	if code != http.StatusSeeOther || !strings.HasPrefix(header.Get("Location"), "/drafts/") {
		t.Fatalf("page reply = %d %q", code, header.Get("Location"))
	}

	a.m.exec(t, "INSERT INTO open_loops (thread_key, account, state, last_message_id, since, counterpart, subject, updated_ts) VALUES ('dinner', 'work', 'owe_reply', ?, 100, 'pat@x.test', 'Dinner', 100)", id)
	code, body, _ = mrvDo(t, a.client, http.MethodGet, a.base+"/loops", nil)
	if code != http.StatusOK || !strings.Contains(body, "Dinner") {
		t.Fatalf("loops page = %d: %.400s", code, body)
	}
	if strings.Contains(body, "Draft reply") || strings.Contains(body, "Draft a reply to mail") {
		t.Fatalf("loops page still offers a paid draft run")
	}
	if !strings.Contains(body, fmt.Sprintf(`href="/messages/%d">`, id)) || !strings.Contains(body, "Open message") {
		t.Fatalf("loops page has no link to the message")
	}
}

func TestMailMessageActionsAreNotAssistantTools(t *testing.T) {
	t.Parallel()
	m := mrvMailFixture(t, "live")
	for _, name := range []string{"mail_message_archive", "mail_message_read", "mail_message_flag"} {
		result := toolResult(t, name, map[string]any{"message_id": 1})
		if !strings.Contains(strings.ToLower(result), "unknown") {
			t.Fatalf("assistant call to %s = %q, want an unknown tool", name, result)
		}
	}
	for _, w := range m.fake.Writes {
		if w.Verb == "STORE" || w.Verb == "COPY" || w.Verb == "MOVE" {
			t.Fatalf("an assistant call wrote %v", w)
		}
	}
}

package main

import (
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/mailtest"
	"github.com/jeiang/atrium/internal/testutil/llmstub"
)

func mailEffectsFixture(t *testing.T, mode, preset string, fake *mailtest.FakeClient) *mailSafety {
	t.Helper()
	dir, _, f := moduleFixture(t, flagsExcept("", ""))
	f.app = newAppWith(f.lookup, f.settings, f.state, nil, func(config.Config) mail.OpenFunc { return fake.Open }, nil, quietLog)
	srv := llmstub.Server(t, nil)
	setEnv(t, "ATRIUM_MAIL_MAIL_CHAT_URL", srv.URL+"/v1")
	setEnv(t, "ATRIUM_MAIL_MAIL_EMBED_URL", "http://127.0.0.1:1/v1")
	setEnv(t, settingEnv(mail.KeyMode), mode)
	setEnv(t, "ATRIUM_HOST_CONFIG", accountsFile(t, dir, mailAccount{"work", preset, "atrium.test@example.test", "app-pass", false}))
	return &mailSafety{dir: dir, app: f.app, fake: fake, path: filepath.Join(dir, "atrium.db")}
}

func TestMailSnoozeReturnStaysPendingInShadow(t *testing.T) {
	fake := mailtest.NewFakeClient()
	inbox := fake.AddBox("INBOX", mailtest.NewBox(1))
	fake.AddBox("Snoozed", mailtest.NewBox(7))
	h, x := mailtest.RawWith("Snoozed statement", "statements@bank.test", "snoozed-1", "Your statement is ready.")
	inbox.Add(h, x, []string{`\Seen`}, nil)
	h, x = mailtest.RawWith("Snoozed receipt", "shop@store.test", "snoozed-2", "Your receipt.")
	fake.Boxes["Snoozed"].Box.Add(h, x, []string{`\Seen`}, nil)
	m := mailEffectsFixture(t, "shadow", "icloud", fake)

	m.run(t, "sync")
	id := m.messageID(t, "Snoozed receipt")
	m.exec(t, "INSERT INTO snoozes (proposal_id, account, message_id, imap_name, uidvalidity, uid, due_ts, state, created_ts) "+
		"VALUES (0, 'work', ?, 'Snoozed', 7, 1, ?, 'pending', ?)", id, mail.WallClock()-60, mail.WallClock()-120)

	m.run(t, "sync")
	if state := m.scalar(t, "SELECT state FROM snoozes WHERE message_id = ?", id); state != "pending" {
		t.Fatalf("a shadow snooze is %s, want pending", state)
	}
	if got := m.verbs(); len(got) != 0 {
		t.Fatalf("a shadow snooze return wrote to IMAP: %v", got)
	}
	if state := m.scalar(t, "SELECT state FROM effects WHERE origin = 'mail_auto' AND operation = 'snooze_return'"); state != "skipped" {
		t.Fatalf("the shadow return effect is %s, want skipped", state)
	}
}

func TestMailSnoozeReturnAndSettlementShareOneTransaction(t *testing.T) {
	due := func(t *testing.T) (*mailSafety, int64) {
		t.Helper()
		fake := mailtest.NewFakeClient()
		fake.AddBox("INBOX", mailtest.NewBox(1))
		fake.AddBox("Snoozed", mailtest.NewBox(7))
		h, x := mailtest.RawWith("Snoozed receipt", "shop@store.test", "snoozed-2", "Your receipt.")
		fake.Boxes["Snoozed"].Box.Add(h, x, []string{`\Seen`}, nil)
		m := mailEffectsFixture(t, "live", "icloud", fake)
		m.run(t, "sync")
		id := m.messageID(t, "Snoozed receipt")
		m.exec(t, "INSERT INTO snoozes (proposal_id, account, message_id, imap_name, uidvalidity, uid, due_ts, state, created_ts) "+
			"VALUES (0, 'work', ?, 'Snoozed', 7, 1, ?, 'pending', ?)", id, mail.WallClock()-60, mail.WallClock()-120)
		return m, id
	}

	t.Run("refused", func(t *testing.T) {
		m, id := due(t)
		m.exec(t, "CREATE TRIGGER refuse_snooze_settle BEFORE UPDATE ON snoozes BEGIN SELECT RAISE(ABORT, 'snooze update refused'); END")
		if ran := runApp(t, "sync"); ran.code == cli.ExitOK {
			t.Fatalf("a pass whose snooze update was refused exited 0")
		}
		if state := m.scalar(t, "SELECT state FROM effects WHERE origin = 'mail_auto' AND operation = 'snooze_return'"); state != "intent" {
			t.Fatalf("the return effect settled as %s although the snooze update failed, want intent", state)
		}
		if state := m.scalar(t, "SELECT state FROM snoozes WHERE message_id = ?", id); state != "pending" {
			t.Fatalf("the snooze is %s after the refused settlement, want pending", state)
		}
	})

	t.Run("settled", func(t *testing.T) {
		m, id := due(t)
		m.run(t, "sync")
		if state := m.scalar(t, "SELECT state FROM effects WHERE origin = 'mail_auto' AND operation = 'snooze_return'"); state != "done" {
			t.Fatalf("the return effect is %s, want done", state)
		}
		if state := m.scalar(t, "SELECT state FROM snoozes WHERE message_id = ?", id); state != "returned" {
			t.Fatalf("the snooze is %s after the return, want returned", state)
		}
		if n := len(m.fake.Boxes["INBOX"].Box.Messages); n != 1 {
			t.Fatalf("the inbox holds %d messages after the return, want 1", n)
		}
	})
}

func (m *mailSafety) ruleFor(t *testing.T, account, dest, from string) {
	t.Helper()
	path := filepath.Join(m.dir, "rules.json")
	body := `{"rules":[{"id":"bank","account":"` + account + `","action":"file","dest":"` + dest + `",` +
		`"when":[{"field":"from_addr","op":"eq","value":"` + from + `"}]}]}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	m.run(t, "rules", "import", path)
}

func (m *mailSafety) effectIDs(t *testing.T, query string, args ...any) []string {
	t.Helper()
	joined := m.scalar(t, query, args...)
	if joined == "<nil>" || joined == "" {
		return nil
	}
	return strings.Split(joined, ",")
}

func TestMailProposalMoveWithUnknownCopyBlocksReapprovalUntilResolved(t *testing.T) {
	fake := mailtest.NewFakeClient()
	inbox := fake.AddBox("INBOX", mailtest.NewBox(1))
	fake.AddBox("Bills", mailtest.NewBox(3))
	h, x := mailtest.RawWith("Bank statement", "statements@bank.test", "unknown-copy", "Your statement is ready.")
	inbox.Add(h, x, []string{`\Seen`}, nil)
	m := mailEffectsFixture(t, "live", "icloud", fake)
	m.ruleFor(t, "work", "Bills", "statements@bank.test")
	m.run(t, "sync")
	m.run(t, "live", "rebaseline")
	m.run(t, "live", "backlog")
	proposal := m.scalar(t, "SELECT CAST(id AS TEXT) FROM proposals WHERE kind = 'inbox_backlog' AND status = 'pending' ORDER BY id DESC LIMIT 1")
	if proposal == "<nil>" {
		t.Fatalf("no pending inbox_backlog proposal")
	}

	m.fake.FailWrite = func(w mailtest.FakeWrite) error {
		if w.Verb == "COPY" {
			return &mail.WriteInterrupted{Command: "UID COPY", Err: errors.New("connection reset by peer")}
		}
		return nil
	}
	if ran := runApp(t, "proposals", "approve", proposal); ran.code == cli.ExitOK {
		t.Fatalf("approving a move whose copy was interrupted exited 0")
	}
	if state := m.scalar(t, "SELECT state FROM effects WHERE origin = 'proposal' AND operation = 'move'"); state != "unknown" {
		t.Fatalf("the interrupted proposal move is %s, want unknown", state)
	}
	if status := m.scalar(t, "SELECT status FROM proposals WHERE id = ?", proposal); status != "failed" {
		t.Fatalf("the proposal after an unknown move is %s, want failed", status)
	}
	if ran := runApp(t, "proposals", "approve", proposal); ran.code == cli.ExitOK || !strings.Contains(ran.stderr, "unresolved write") {
		t.Fatalf("re-approval with an unresolved write = %d %q", ran.code, ran.stderr)
	}
	before := m.copies()
	m.run(t, "sync")
	if after := m.copies(); after != before {
		t.Fatalf("a sync after an unknown move made %d copies, want none", after-before)
	}
	m.run(t, "proposals", "resolve", proposal, "--outcome", "not-applied")
	if effective := m.scalar(t, "SELECT effective_outcome FROM effect_resolutions WHERE effect_id = "+
		"(SELECT id FROM effects WHERE origin = 'proposal' AND operation = 'move') ORDER BY id DESC LIMIT 1"); effective != "failed" {
		t.Fatalf("the resolved move's effective outcome is %s, want failed", effective)
	}
	m.fake.FailWrite = nil
	m.run(t, "proposals", "approve", proposal)
	if n := len(m.fake.Boxes["Bills"].Box.Messages); n != 1 {
		t.Fatalf("the destination holds %d messages after re-approval, want 1", n)
	}
	if n := len(m.fake.Boxes["INBOX"].Box.Messages); n != 0 {
		t.Fatalf("the inbox holds %d messages after the move, want 0", n)
	}
}

func TestMailGmailRelabelInterruptedRemoveKeepsTheCompletedAdd(t *testing.T) {
	fake := mailtest.NewFakeClient()
	fake.Gmail = true
	all := fake.AddBox("[Gmail]/All Mail", mailtest.NewBox(9), `\All`)
	fake.AddBox("[Gmail]/Trash", mailtest.NewBox(10), `\Trash`)
	m := mailEffectsFixture(t, "live", "gmail", fake)
	m.ruleFor(t, "work", "Bills", "statements@bank.test")
	m.run(t, "sync")
	h, x := mailtest.RawWith("Bank statement", "statements@bank.test", "gmail-partial", "Your statement is ready.")
	uid := all.Add(h, x, []string{`\Seen`}, []string{`\Inbox`})
	m.fake.FailWrite = func(w mailtest.FakeWrite) error {
		if w.Verb == "STORE" && len(w.Args) > 0 && w.Args[0] == "-X-GM-LABELS" {
			return errors.New("connection reset by peer")
		}
		return nil
	}
	if ran := runApp(t, "sync"); ran.code == cli.ExitOK {
		t.Fatalf("a sync whose relabel was interrupted exited 0")
	}
	if state := m.scalar(t, "SELECT state FROM effects WHERE origin = 'mail_auto' AND operation = 'file'"); state != "partial" {
		t.Fatalf("the interrupted relabel is %s, want partial", state)
	}
	after := m.scalar(t, "SELECT after_json FROM effects WHERE origin = 'mail_auto' AND operation = 'file'")
	if !strings.Contains(after, `"add":["Bills"]`) || !strings.Contains(after, `"remove":[]`) {
		t.Fatalf("the partial relabel recorded %s, want the completed add and an empty remove", after)
	}
	labels := fake.Boxes["[Gmail]/All Mail"].Box.Messages[uid].Labels
	if !slices.Contains(labels, "Bills") || !slices.Contains(labels, `\Inbox`) {
		t.Fatalf("the message labels after the interrupted remove = %v, want Bills and \\Inbox", labels)
	}
}

func TestMailAutomaticPartialMoveUndoesOnlyTheCopiedMessages(t *testing.T) {
	fake := mailtest.NewFakeClient()
	inbox := fake.AddBox("INBOX", mailtest.NewBox(1))
	fake.AddBox("Bills", mailtest.NewBox(3))
	m := mailEffectsFixture(t, "live", "icloud", fake)
	setEnv(t, settingEnv(mail.KeyWriteCap), strconv.Itoa(mail.TuningMailopsBatch+1))
	m.ruleFor(t, "work", "Bills", "statements@bank.test")
	m.run(t, "sync")
	var uids []uint32
	for i := 1; i <= mail.TuningMailopsBatch+1; i++ {
		h, x := mailtest.RawWith("Statement "+strconv.Itoa(i), "statements@bank.test", "partial-"+strconv.Itoa(i), "Your statement is ready.")
		uids = append(uids, inbox.Add(h, x, []string{`\Seen`}, nil))
	}
	m.fake.FailWrite = func(w mailtest.FakeWrite) error {
		if w.Verb == "STORE" && len(w.Args) > 1 && w.Args[1] == `\Deleted` {
			return errors.New("store refused")
		}
		return nil
	}
	if ran := runApp(t, "sync"); ran.code == cli.ExitOK {
		t.Fatalf("a sync whose move was refused after the copy exited 0")
	}
	if n := m.count(t, "SELECT COUNT(*) FROM effects WHERE origin = 'mail_auto' AND operation = 'file' AND state = 'partial'"); n != mail.TuningMailopsBatch {
		t.Fatalf("%d partial moves, want %d", n, mail.TuningMailopsBatch)
	}
	if n := m.count(t, "SELECT COUNT(*) FROM effects WHERE origin = 'mail_auto' AND operation = 'file' AND state = 'skipped'"); n != 1 {
		t.Fatalf("%d skipped moves, want 1", n)
	}
	if n := len(fake.Boxes["Bills"].Box.Messages); n != mail.TuningMailopsBatch {
		t.Fatalf("the destination holds %d messages after the refused store, want %d", n, mail.TuningMailopsBatch)
	}
	if n := len(fake.Boxes["INBOX"].Box.Messages); n != mail.TuningMailopsBatch+1 {
		t.Fatalf("the inbox holds %d messages after the refused store, want %d", n, mail.TuningMailopsBatch+1)
	}
	m.fake.FailWrite = nil
	for _, id := range m.effectIDs(t, "SELECT group_concat(CAST(id AS TEXT), ',') FROM "+
		"(SELECT id FROM effects WHERE origin = 'mail_auto' AND operation = 'file' AND state = 'partial' ORDER BY id)") {
		m.run(t, "live", "undo", id)
	}
	if n := len(fake.Boxes["Bills"].Box.Messages); n != 0 {
		t.Fatalf("the destination holds %d messages after the undos, want 0", n)
	}
	untouched := uids[mail.TuningMailopsBatch]
	if _, ok := fake.Boxes["INBOX"].Box.Messages[untouched]; !ok {
		t.Fatalf("the last message was moved although its move was never attempted")
	}
	for _, w := range fake.Writes {
		if w.Verb == "COPY" && slices.Contains(w.UIDs, untouched) {
			t.Fatalf("a copy included the untouched last message: %v", w)
		}
	}
	if state := m.scalar(t, "SELECT state FROM effects WHERE origin = 'mail_auto' AND operation = 'file' AND message_id = ?", m.messageID(t, "Statement "+strconv.Itoa(mail.TuningMailopsBatch+1))); state != "skipped" {
		t.Fatalf("the last message's move is %s, want skipped", state)
	}
}

func TestMailProposalMoveCreatesAMissingFolderAsItsOwnEffect(t *testing.T) {
	fake := mailtest.NewFakeClient()
	inbox := fake.AddBox("INBOX", mailtest.NewBox(1))
	h, x := mailtest.RawWith("Bank statement", "statements@bank.test", "create-folder", "Your statement is ready.")
	inbox.Add(h, x, []string{`\Seen`}, nil)
	m := mailEffectsFixture(t, "live", "icloud", fake)
	m.ruleFor(t, "work", "Archive", "statements@bank.test")
	m.run(t, "sync")
	m.run(t, "live", "rebaseline")
	m.run(t, "live", "backlog")
	proposal := m.scalar(t, "SELECT CAST(id AS TEXT) FROM proposals WHERE kind = 'inbox_backlog' AND status = 'pending' ORDER BY id DESC LIMIT 1")
	if proposal == "<nil>" {
		t.Fatalf("no pending inbox_backlog proposal")
	}
	m.run(t, "proposals", "approve", proposal)
	if state := m.scalar(t, "SELECT state FROM effects WHERE origin = 'proposal' AND operation = 'create_folder'"); state != "done" {
		t.Fatalf("the folder creation is %s, want done", state)
	}
	undo := m.scalar(t, "SELECT undo_json FROM effects WHERE origin = 'proposal' AND operation = 'create_folder'")
	if !strings.Contains(undo, `"op":"delete_folder"`) {
		t.Fatalf("the folder creation's undo is %s, want a delete_folder", undo)
	}
	if state := m.scalar(t, "SELECT state FROM effects WHERE origin = 'proposal' AND operation = 'move'"); state != "done" {
		t.Fatalf("the move is %s, want done", state)
	}
	var created, subscribed bool
	for _, w := range fake.Writes {
		created = created || w.Verb == "CREATE"
		subscribed = subscribed || w.Verb == "SUBSCRIBE"
	}
	if !created || !subscribed {
		t.Fatalf("the folder setup wrote %v, want CREATE and SUBSCRIBE", m.verbs())
	}
	if n := len(fake.Boxes["Archive"].Box.Messages); n != 1 {
		t.Fatalf("the new folder holds %d messages, want 1", n)
	}
}

func TestMrvDraftSendUnknownSentAppendLeavesTheMailSentAndUnsendable(t *testing.T) {
	m := mrvMailFixture(t, "shadow")
	smtp := mrvSMTPServer(t)
	toolResult(t, "draft_email_create", map[string]any{"account": "icloud", "to": []any{"friend@x.test"}, "subject": "Lunch plans", "body": "Noon works."})
	dbPath := filepath.Join(m.dir, "atrium.db")
	id := mrvScalar(t, dbPath, "SELECT CAST(id AS TEXT) FROM drafts WHERE subject = 'Lunch plans'")
	draft := "/drafts/" + id
	status := func() string { return mrvScalar(t, dbPath, "SELECT status FROM drafts WHERE id = ?", id) }
	hx := []string{"HX-Request", "true"}
	base, _, _ := startServeApp(t, m.app, &lockedBuffer{})
	client := mrvClient(t)
	revision := func() string {
		t.Helper()
		code, page, _ := mrvDo(t, client, http.MethodGet, base+draft, nil)
		found := regexp.MustCompile(`name="revision" value="([0-9]+)"`).FindStringSubmatch(page)
		if code != http.StatusOK || found == nil {
			t.Fatalf("draft page = %d without a revision: %.400s", code, page)
		}
		return found[1]
	}

	m.fake.FailWrite = func(w mailtest.FakeWrite) error {
		if w.Verb == "APPEND" {
			return &mail.WriteInterrupted{Command: "APPEND", Err: errors.New("connection reset by peer")}
		}
		return nil
	}
	code, body, _ := mrvDo(t, client, http.MethodPost, base+draft+"/send", url.Values{"revision": {revision()}}, hx...)
	if code != http.StatusOK || !strings.Contains(body, "Sent copy failed") {
		t.Fatalf("send with an interrupted Sent copy = %d: %.400s", code, body)
	}
	if got := status(); got != "sent" {
		t.Fatalf("the draft after a sent mail with an unknown copy is %q, want sent", got)
	}
	if n := len(smtp.sent()); n != 1 {
		t.Fatalf("%d deliveries, want 1", n)
	}
	appendID := mrvScalar(t, dbPath, "SELECT CAST(id AS TEXT) FROM effects WHERE operation = 'sent_append' AND direction = 'forward'")
	if state := mrvScalar(t, dbPath, "SELECT state FROM effects WHERE id = ?", appendID); state != "unknown" {
		t.Fatalf("the Sent copy is %s, want unknown", state)
	}
	smtpID := mrvScalar(t, dbPath, "SELECT CAST(id AS TEXT) FROM effects WHERE operation = 'smtp_send' AND direction = 'forward'")
	rev := mrvScalar(t, dbPath, "SELECT CAST(revision AS TEXT) FROM drafts WHERE id = ?", id)
	for _, r := range []string{rev, strconv.Itoa(mrvAtoi(t, rev) + 1)} {
		if code, _, _ := mrvDo(t, client, http.MethodPost, base+draft+"/send", url.Values{"revision": {r}}, hx...); code != http.StatusConflict {
			t.Fatalf("resend at revision %s = %d, want 409", r, code)
		}
	}
	if n := len(smtp.sent()); n != 1 {
		t.Fatalf("an unsendable draft was sent again: %d deliveries", n)
	}
	if ran := runApp(t, "effects", "resolve", appendID, "--outcome", "not-applied"); ran.code != cli.ExitOK {
		t.Fatalf("effects resolve: %d %s", ran.code, ran.stderr)
	}
	if state := mrvScalar(t, dbPath, "SELECT state FROM effects WHERE id = ?", smtpID); state != "done" {
		t.Fatalf("the SMTP effect after resolving the copy is %s, want done", state)
	}
	if effective := mrvScalar(t, dbPath, "SELECT effective_outcome FROM effect_resolutions WHERE effect_id = ? ORDER BY id DESC LIMIT 1", appendID); effective != "failed" {
		t.Fatalf("the copy's resolution is %s, want failed", effective)
	}
	if got := status(); got != "sent" {
		t.Fatalf("the draft after resolving the copy is %q, want sent", got)
	}
	for _, r := range []string{rev, strconv.Itoa(mrvAtoi(t, rev) + 1)} {
		if code, _, _ := mrvDo(t, client, http.MethodPost, base+draft+"/send", url.Values{"revision": {r}}, hx...); code != http.StatusConflict {
			t.Fatalf("resend after resolving the copy at revision %s = %d, want 409", r, code)
		}
	}
}

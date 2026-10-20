package mailtools_test

import (
	"errors"
	"testing"

	"github.com/jeiang/atrium/internal/assistant/tool"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/mail/drafts"
)

func draftWorld(t *testing.T) (*db.DB, *tool.Context, int64) {
	t.Helper()
	d := newDB(t)
	exec(t, d, "INSERT INTO threads (id, kind, effort, created_at, updated_at) VALUES (1, 'chat', 'low', 0, 0)")
	exec(t, d, "INSERT INTO runs (id, thread_id, kind, model, effort, state, started_at) VALUES (1, 1, 'chat', 'm', 'low', 'running', 0)")
	res, err := d.Write.Exec("INSERT INTO messages (account, mailbox_id, uidvalidity, uid, digest, first_seen, subject_tmpl, from_addr, from_name, subject, msgid, to_addrs) " +
		"VALUES ('icloud', 1, 1, 1, 'd1', 0, '', 'friend@x.test', 'Friend', 'Lunch?', '<orig@x.test>', '[\"me@icloud.test\"]')")
	if err != nil {
		t.Fatal(err)
	}
	mid, _ := res.LastInsertId()
	exec(t, d, "INSERT INTO message_fts (rowid, subject, sender, body) VALUES (?, 'Lunch?', 'friend@x.test', '')", mid)
	return d, ctxFor(d, config.Config{CacheDir: t.TempDir()}, nil), mid
}

func draftRow(t *testing.T, d *db.DB, id int64) drafts.Row {
	t.Helper()
	row, err := (&drafts.Store{DB: d}).Get(t.Context(), id)
	if err != nil || row == nil {
		t.Fatalf("draft %d: %v, %v", id, row, err)
	}
	return *row
}

func TestDraftToolCreatesWithThreadingAndNeverSends(t *testing.T) {
	d, tc, mid := draftWorld(t)
	out := mustCall(t, tc, "draft_email_create",
		`{"account": "icloud", "to": ["friend@x.test"], "subject": "Re: Lunch?", "body": "Yes", "in_reply_to_message_id": `+itoa(mid)+`}`)
	id := get(t, out, "draft_id").(int64)
	if get(t, out, "status") != "draft" || get(t, out, "note") != "Draft saved only. Nothing is sent: the operator reviews and sends it from the Drafts page." {
		t.Errorf("out = %v", out)
	}
	row := draftRow(t, d, id)
	if row.Status != "draft" || row.InReplyTo != "<orig@x.test>" || row.Refs != "<orig@x.test>" || row.SentAt != nil || row.SMTPResult != nil {
		t.Errorf("row = %+v", row)
	}
	if row.ThreadID == nil || *row.ThreadID != 1 {
		t.Errorf("thread = %v", row.ThreadID)
	}
	upd := mustCall(t, tc, "draft_email_update", `{"draft_id": `+itoa(id)+`, "body": "Yes please"}`)
	if got := draftRow(t, d, get(t, upd, "draft_id").(int64)).Body; got != "Yes please" {
		t.Errorf("body = %q", got)
	}
	if get(t, upd, "status") != "draft" {
		t.Errorf("update = %v", upd)
	}
	_, err := call(t, tc, "draft_email_create", `{"account": "icloud", "to": ["a@x.test\r\nBcc: z@x.test"], "subject": "s", "body": "b"}`)
	wantToolError(t, err, "")
}

func TestDraftToolErrors(t *testing.T) {
	_, tc, _ := draftWorld(t)
	for _, c := range []struct{ tool, args, want string }{
		{"draft_email_create", `{"to": ["a@x.test"], "subject": "s", "body": "b"}`, "account is required"},
		{"draft_email_create", `{"account": "icloud", "to": ["a@x.test"], "body": "b"}`, "subject is required"},
		{"draft_email_create", `{"account": "icloud", "to": ["a@x.test"], "subject": "s", "body": "b", "in_reply_to_message_id": "1"}`,
			"in_reply_to_message_id must be an integer message_id from mail_search"},
		{"draft_email_create", `{"account": "icloud", "to": ["a@x.test"], "subject": "s", "body": "b", "in_reply_to_message_id": true}`,
			"in_reply_to_message_id must be an integer message_id from mail_search"},
		{"draft_email_create", `{"account": "icloud", "to": [], "subject": "s", "body": "b"}`, "to is required"},
		{"draft_email_create", `{"account": "icloud", "to": ["a@x.test"], "subject": "s", "body": " "}`, "body is required"},
		{"draft_email_create", `{"account": "icloud", "to": ["a@x.test"], "subject": "s", "body": "b", "in_reply_to_message_id": 999}`, "no message 999"},
		{"draft_email_update", `{}`, "draft_id must be an integer"},
		{"draft_email_update", `{"draft_id": true}`, "draft_id must be an integer"},
		{"draft_email_update", `{"draft_id": 77, "body": "x"}`, "no draft 77"},
	} {
		_, err := call(t, tc, c.tool, c.args)
		wantToolError(t, err, c.want)
	}
}

func TestDraftUpdateWronglyTypedFieldsRaisePythonClasses(t *testing.T) {
	_, tc, _ := draftWorld(t)
	out := mustCall(t, tc, "draft_email_create", `{"account": "gmail", "to": ["a@x.test"], "subject": "Hi", "body": "body"}`)
	id := itoa(get(t, out, "draft_id").(int64))
	for args, class := range map[string]string{
		`{"draft_id": ` + id + `, "subject": 5}`:                  "AttributeError",
		`{"draft_id": ` + id + `, "in_reply_to_message_id": [1]}`: "ProgrammingError",
		`{"draft_id": ` + id + `, "account": ["icloud"]}`:         "TypeError",
		`{"draft_id": ` + id + `, "account": {"a": 1}}`:           "TypeError",
	} {
		_, err := call(t, tc, "draft_email_update", args)
		var raised *tool.Raised
		if !errors.As(err, &raised) || raised.Class != class {
			t.Errorf("%s: err = %v (%T)", args, err, err)
		}
	}
}

func TestDraftToolsNeverRecordAgentWrites(t *testing.T) {
	d, tc, mid := draftWorld(t)
	out := mustCall(t, tc, "draft_email_create",
		`{"account": "icloud", "to": ["friend@x.test"], "subject": "Re: Lunch?", "body": "Yes", "in_reply_to_message_id": `+itoa(mid)+`}`)
	mustCall(t, tc, "draft_email_update", `{"draft_id": `+itoa(get(t, out, "draft_id").(int64))+`, "body": "Yes please"}`)
	var n int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM agent_writes").Scan(&n); err != nil || n != 0 {
		t.Errorf("agent_writes = %d, %v", n, err)
	}
}

func TestDraftUpdateRefusesSentDrafts(t *testing.T) {
	d, tc, _ := draftWorld(t)
	out := mustCall(t, tc, "draft_email_create", `{"account": "gmail", "to": ["a@x.test"], "subject": "Hi", "body": "body"}`)
	id := get(t, out, "draft_id").(int64)
	exec(t, d, "UPDATE drafts SET status = 'sent' WHERE id = ?", id)
	_, err := call(t, tc, "draft_email_update", `{"draft_id": `+itoa(id)+`, "to": ["attacker@x.test"]}`)
	wantToolError(t, err, "draft is sent, only drafts can be edited")
}

func TestDraftUpdateChangesRecipientsForReview(t *testing.T) {
	d, tc, _ := draftWorld(t)
	out := mustCall(t, tc, "draft_email_create", `{"account": "gmail", "to": ["a@x.test"], "subject": "Hi", "body": "body"}`)
	id := get(t, out, "draft_id").(int64)
	before := draftRow(t, d, id)
	mustCall(t, tc, "draft_email_update", `{"draft_id": `+itoa(id)+`, "to": ["attacker@x.test"]}`)
	after := draftRow(t, d, id)
	if after.ToAddrs != "attacker@x.test" || after.UpdatedAt < before.UpdatedAt || after.Status != "draft" {
		t.Errorf("after = %+v", after)
	}
}

func TestDraftCreateValidatesBeforeAHugeReplyIDOverflows(t *testing.T) {
	d, tc, _ := draftWorld(t)
	_, err := call(t, tc, "draft_email_create", `{"account": "yahoo", "to": ["a@x.test"], "subject": "s", "body": "b", "in_reply_to_message_id": 99999999999999999999}`)
	wantToolError(t, err, "account must be one of icloud, gmail")
	_, err = call(t, tc, "draft_email_create", `{"account": "icloud", "to": [], "subject": "s", "body": "b", "in_reply_to_message_id": 99999999999999999999}`)
	wantToolError(t, err, "to is required")
	_, err = call(t, tc, "draft_email_create", `{"account": "icloud", "to": ["a@x.test"], "subject": "s", "body": "b", "in_reply_to_message_id": 99999999999999999999}`)
	var raised *tool.Raised
	if !errors.As(err, &raised) || raised.Class != "OverflowError" {
		t.Errorf("err = %v (%T)", err, err)
	}
	var n int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM drafts").Scan(&n); err != nil || n != 0 {
		t.Errorf("%d drafts stored (%v)", n, err)
	}
}

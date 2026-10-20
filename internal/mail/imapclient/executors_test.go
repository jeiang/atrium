package imapclient

import (
	"context"
	"errors"
	"testing"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/db/dbtest"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/mailops"
	"github.com/jeiang/atrium/internal/proposals"
	"github.com/jeiang/atrium/internal/proposals/executors"
	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/safety"
	"github.com/jeiang/atrium/internal/testutil/imapscript"
)

const gmailLogin = `S: {gmail.greeting}
C: {t} CAPABILITY
S: * CAPABILITY {gmail.caps}
S: {t} OK Thats all she wrote! a1b2c3d4e5f6
C: {t} LOGIN "atrium.test@gmail.com" "app password"
S: {t} OK [CAPABILITY {gmail.authcaps}] atrium.test@gmail.com authenticated (Success)
`

const gmailLogout = `C: {t} LOGOUT
S: * BYE LOGOUT Requested
S: {t} OK 73 good day (Success)
`

func gmail(t *testing.T, script string, env config.Env) mail.Client {
	t.Helper()
	client, _ := session(t, imapscript.MustParse(t, script), "atrium.test@gmail.com", "app password", env)
	return client
}

// sessions opens each scripted client in turn, one per mailops.Session.
func sessions(t *testing.T, clients ...mail.Client) mailops.Opener {
	return func(ctx context.Context, _ string) (mail.Client, error) {
		if len(clients) == 0 {
			t.Fatal("no scripted session left")
		}
		client := clients[0]
		clients = clients[1:]
		return client, client.Connect(ctx)
	}
}

func seeded(t *testing.T, account, box string, validity int64, rows ...[2]any) *db.DB {
	t.Helper()
	d := dbtest.Schema(t)
	exec := func(q string, args ...any) {
		if _, err := d.Write.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec("INSERT INTO accounts (name, kind) VALUES (?, ?)", account, account)
	exec("INSERT INTO mailboxes (id, account, name, imap_name, role) VALUES (1, ?, ?, ?, 'inbox')", account, box, box)
	for i, r := range rows {
		exec("INSERT INTO messages (id, account, mailbox_id, uidvalidity, uid, msgid, digest, first_seen) VALUES (?, ?, 1, ?, ?, ?, ?, 0)",
			i+1, account, validity, r[0], r[1], "d"+r[1].(string))
	}
	return d
}

func approve(t *testing.T, d *db.DB, open mailops.Opener, kind, account string, payload func([]any) py.Dict, ids ...int64) *proposals.Proposal {
	t.Helper()
	ctx := context.Background()
	snaps, err := mailops.Snapshots(ctx, d.Read, ids)
	must(t, err)
	list := make([]any, len(snaps))
	for i, s := range snaps {
		list[i] = s.Dict()
	}
	id, _, err := proposals.Create(ctx, d, kind, &account, "title", payload(list), py.Dict{}, "fp", 1)
	must(t, err)
	r := proposals.NewRegistry()
	executors.Register(r, &executors.Executors{DB: d, Open: open, Now: func() float64 { return 2 }}, nil)
	r.Sync = nil
	p, err := (&proposals.Pipeline{DB: d, Registry: r, Now: func() float64 { return 2 }}).ApproveAndApply(ctx, id, nil)
	must(t, err)
	return p
}

func actionsOf(t *testing.T, d *db.DB, id int64) string {
	t.Helper()
	logged, err := proposals.Actions(context.Background(), d.Read, id)
	must(t, err)
	out := ""
	for _, a := range logged {
		out += py.JSONDumps(a.Action) + " undo " + py.JSONDumps(a.Undo) + "\n"
	}
	return out
}

const verifyInbox = `C/ ^{t} STATUS INBOX \((UIDVALIDITY|UIDNEXT|MESSAGES|HIGHESTMODSEQ)( (UIDVALIDITY|UIDNEXT|MESSAGES|HIGHESTMODSEQ)){3}\)$
S: * STATUS INBOX (MESSAGES 2 UIDNEXT 303 UIDVALIDITY 7 HIGHESTMODSEQ 5)
S: {t} OK STATUS completed
C: {t} EXAMINE INBOX
S: {t} OK [READ-ONLY] EXAMINE completed
C: {t} UID SEARCH UID 301:302
S: * SEARCH 301 302
S: {t} OK SEARCH completed
C/ ^{t} UID FETCH 301:302 \((UID|FLAGS|BODY\.PEEK\[HEADER\])( (UID|FLAGS|BODY\.PEEK\[HEADER\])){2}\)$
S: * 1 FETCH (UID 301 FLAGS (\Seen) BODY[HEADER] {24}
S: Message-ID: <a@x.test>
S: )
S: * 2 FETCH (UID 302 FLAGS (\Seen) BODY[HEADER] {24}
S: Message-ID: <b@x.test>
S: )
S: {t} OK FETCH completed
C: {t} LIST "" "*"
S: * LIST (\HasNoChildren) "/" INBOX
S: {t} OK LIST completed
`

func backlogPayload(dest string) func([]any) py.Dict {
	return func(snaps []any) py.Dict {
		return py.Dict{{Key: "folder", Value: "INBOX"}, {Key: "groups", Value: []any{py.Dict{{Key: "dest", Value: dest}, {Key: "messages", Value: snaps}}}}}
	}
}

func TestExecuteMovesWire(t *testing.T) {
	d := seeded(t, config.ICloud, "INBOX", 7, [2]any{301, "a@x.test"}, [2]any{302, "b@x.test"})
	client, _ := icloud(t, icloudLogin+verifyInbox+`C: {t} CREATE "Receipts/Shop"
S: {t} OK CREATE completed
C: {t} SUBSCRIBE "Receipts/Shop"
S: {t} OK SUBSCRIBE completed
C: {t} SELECT INBOX
S: * OK [UIDVALIDITY 7] UIDs valid
S: {t} OK [READ-WRITE] SELECT completed
C: {t} UID COPY 301:302 "Receipts/Shop"
S: {t} OK [COPYUID 9 301:302 1:2] Done
C: {t} UID STORE 301:302 +FLAGS.SILENT (\Deleted)
S: {t} OK STORE completed
C: {t} UID EXPUNGE 301:302
S: * 1 EXPUNGE
S: * 1 EXPUNGE
S: {t} OK EXPUNGE completed
`+logout, shadow)
	p := approve(t, d, sessions(t, client), executors.InboxBacklog, config.ICloud, backlogPayload("Receipts/Shop"), 1, 2)
	if p.Status != proposals.Applied || py.JSONDumps(p.Result) != `{"summary": "2 moved, 0 skipped as changed", "moved": 2, "skipped": [], "destinations": ["Receipts/Shop"]}` {
		t.Fatalf("applied = %s %s", p.Status, py.JSONDumps(p.Result))
	}
	want := `{"op": "create_folder", "imap_name": "Receipts/Shop"} undo {"op": "delete_folder", "account": "icloud", "imap_name": "Receipts/Shop"}
{"op": "move", "account": "icloud", "src_imap": "INBOX", "src_uidvalidity": 7, "dest_imap": "Receipts/Shop", "dest_uidvalidity": 9, "items": [{"id": 1, "msgid": "a@x.test", "uid": 301, "dest_uid": 1}, {"id": 2, "msgid": "b@x.test", "uid": 302, "dest_uid": 2}]} undo {"op": "move_back", "account": "icloud", "src_imap": "INBOX", "dest_imap": "Receipts/Shop", "dest_uidvalidity": 9, "items": [{"id": 1, "msgid": "a@x.test", "uid": 301, "dest_uid": 1}, {"id": 2, "msgid": "b@x.test", "uid": 302, "dest_uid": 2}]}
`
	if got := actionsOf(t, d, p.ID); got != want {
		t.Fatalf("actions =\n%s\nwant\n%s", got, want)
	}
}

func TestUnapprovedExecutorSendsNoWrite(t *testing.T) {
	d := seeded(t, config.ICloud, "INBOX", 7, [2]any{301, "a@x.test"}, [2]any{302, "b@x.test"})
	client, _ := icloud(t, icloudLogin+verifyInbox+logout, shadow)
	ctx := context.Background()
	snaps, err := mailops.Snapshots(ctx, d.Read, []int64{1, 2})
	must(t, err)
	list := make([]any, len(snaps))
	for i, s := range snaps {
		list[i] = s.Dict()
	}
	account := config.ICloud
	payload := backlogPayload("Receipts/Shop")(list)
	e := &executors.Executors{DB: d, Open: sessions(t, client)}
	_, err = e.ExecuteMoves(ctx, &proposals.Proposal{ID: 1, Kind: executors.InboxBacklog, Account: &account, Payload: []byte(py.JSONDumps(payload))}, nil)
	var refused safety.WriteRefused
	if !errors.As(err, &refused) {
		t.Fatalf("unapproved apply = %v", err)
	}
}

func TestUndoDeleteFolderWire(t *testing.T) {
	client, _ := icloud(t, icloudLogin+`C/ ^{t} STATUS "Receipts/Shop" \((UIDVALIDITY|UIDNEXT|MESSAGES|HIGHESTMODSEQ)( (UIDVALIDITY|UIDNEXT|MESSAGES|HIGHESTMODSEQ)){3}\)$
S: * STATUS "Receipts/Shop" (MESSAGES 0 UIDNEXT 3 UIDVALIDITY 9 HIGHESTMODSEQ 4)
S: {t} OK STATUS completed
C: {t} DELETE "Receipts/Shop"
S: {t} OK DELETE completed
`+logout, shadow)
	e := &executors.Executors{DB: dbtest.Schema(t), Open: sessions(t, client)}
	detail, err := e.UndoDeleteFolder(safety.ApprovedWrites(context.Background(), "1"), []byte(`{"op": "delete_folder", "account": "icloud", "imap_name": "Receipts/Shop"}`))
	must(t, err)
	if py.JSONDumps(detail) != `{"deleted": true}` {
		t.Fatalf("detail = %v", detail)
	}
}

const verifyAllMail = `C/ ^{t} STATUS "\[Gmail\]/All Mail" \((UIDVALIDITY|UIDNEXT|MESSAGES|HIGHESTMODSEQ)( (UIDVALIDITY|UIDNEXT|MESSAGES|HIGHESTMODSEQ)){3}\)$
S: * STATUS "[Gmail]/All Mail" (MESSAGES 2 UIDNEXT 103 UIDVALIDITY 1 HIGHESTMODSEQ 9)
S: {t} OK Success
C: {t} EXAMINE "[Gmail]/All Mail"
S: {t} OK [READ-ONLY] [Gmail]/All Mail selected. (Success)
C: {t} UID SEARCH UID 101
S: * SEARCH 101
S: {t} OK SEARCH completed (Success)
C/ ^{t} UID FETCH 101 \((UID|FLAGS|X-GM-LABELS|BODY\.PEEK\[HEADER\])( (UID|FLAGS|X-GM-LABELS|BODY\.PEEK\[HEADER\])){3}\)$
S: * 1 FETCH (UID 101 X-GM-LABELS ("Parent/Small") FLAGS (\Seen) BODY[HEADER] {24}
S: Message-ID: <a@x.test>
S: )
S: {t} OK Success
`

func TestLabelMergeDeletesTheEmptyLabelWire(t *testing.T) {
	d := seeded(t, config.Gmail, config.GmailAllMail, 1, [2]any{101, "a@x.test"})
	client := gmail(t, gmailLogin+verifyAllMail+`C: {t} SELECT "[Gmail]/All Mail"
S: * OK [UIDVALIDITY 1] UIDs valid
S: {t} OK [READ-WRITE] [Gmail]/All Mail selected. (Success)
C: {t} UID STORE 101 +X-GM-LABELS ("Parent")
S: {t} OK Success
C: {t} UID STORE 101 -X-GM-LABELS ("Parent/Small")
S: {t} OK Success
C/ ^{t} STATUS "Parent/Small" \((UIDVALIDITY|UIDNEXT|MESSAGES|HIGHESTMODSEQ)( (UIDVALIDITY|UIDNEXT|MESSAGES|HIGHESTMODSEQ)){3}\)$
S: * STATUS "Parent/Small" (MESSAGES 0 UIDNEXT 1 UIDVALIDITY 3 HIGHESTMODSEQ 9)
S: {t} OK Success
C/ ^{t} STATUS "Parent/Small" \((UIDVALIDITY|UIDNEXT|MESSAGES|HIGHESTMODSEQ)( (UIDVALIDITY|UIDNEXT|MESSAGES|HIGHESTMODSEQ)){3}\)$
S: * STATUS "Parent/Small" (MESSAGES 0 UIDNEXT 1 UIDVALIDITY 3 HIGHESTMODSEQ 9)
S: {t} OK Success
C: {t} DELETE "Parent/Small"
S: {t} OK Success
`+gmailLogout, shadow)
	p := approve(t, d, sessions(t, client), executors.GmailLabelMerge, config.Gmail, func(snaps []any) py.Dict {
		return py.Dict{{Key: "label", Value: "Parent/Small"}, {Key: "into", Value: "Parent"}, {Key: "messages", Value: snaps}}
	}, 1)
	if py.JSONDumps(p.Result) != `{"summary": "1 relabelled, label deleted, 0 skipped as changed", "relabelled": 1, "label_deleted": true, "skipped": []}` {
		t.Fatalf("applied = %s %s", p.Status, py.JSONDumps(p.Result))
	}
	want := `{"op": "gmail_labels", "imap_name": "[Gmail]/All Mail", "uidvalidity": 1, "uids": [101], "ids": [1], "add": ["Parent"], "remove": ["Parent/Small"]} undo {"op": "gmail_labels", "imap_name": "[Gmail]/All Mail", "uidvalidity": 1, "uids": [101], "ids": [1], "add": ["Parent/Small"], "remove": ["Parent"]}
{"op": "delete_label", "imap_name": "Parent/Small"} undo null
`
	if got := actionsOf(t, d, p.ID); got != want {
		t.Fatalf("actions =\n%s\nwant\n%s", got, want)
	}
}

func TestGmailDeletionCopiesToTrashAndUntrashesWire(t *testing.T) {
	d := seeded(t, config.Gmail, config.GmailAllMail, 1, [2]any{101, "a@x.test"})
	apply := gmail(t, gmailLogin+verifyAllMail+`C: {t} LIST "" "*"
S: * LIST (\HasNoChildren) "/" "INBOX"
S: * LIST (\HasChildren \Noselect) "/" "[Gmail]"
S: * LIST (\All \HasNoChildren) "/" "[Gmail]/All Mail"
S: * LIST (\HasNoChildren \Trash) "/" "[Gmail]/Trash"
S: {t} OK Success
C: {t} SELECT "[Gmail]/All Mail"
S: * OK [UIDVALIDITY 1] UIDs valid
S: {t} OK [READ-WRITE] [Gmail]/All Mail selected. (Success)
C: {t} UID COPY 101 "[Gmail]/Trash"
S: {t} OK [COPYUID 5 101 900] (Success)
`+gmailLogout, shadow)
	untrash := gmail(t, gmailLogin+`C/ ^{t} STATUS "\[Gmail\]/Trash" \((UIDVALIDITY|UIDNEXT|MESSAGES|HIGHESTMODSEQ)( (UIDVALIDITY|UIDNEXT|MESSAGES|HIGHESTMODSEQ)){3}\)$
S: * STATUS "[Gmail]/Trash" (MESSAGES 1 UIDNEXT 901 UIDVALIDITY 5 HIGHESTMODSEQ 9)
S: {t} OK Success
C: {t} SELECT "[Gmail]/Trash"
S: * OK [UIDVALIDITY 5] UIDs valid
S: {t} OK [READ-WRITE] [Gmail]/Trash selected. (Success)
C: {t} UID STORE 900 -X-GM-LABELS (\Trash)
S: {t} OK Success
`+gmailLogout, shadow)
	open := sessions(t, apply, untrash)
	p := approve(t, d, open, executors.DeletionRule, config.Gmail, func(snaps []any) py.Dict {
		rule := py.Dict{{Key: "id", Value: "del-x"}, {Key: "account", Value: "gmail"}, {Key: "action", Value: "delete"},
			{Key: "when", Value: []any{py.Dict{{Key: "field", Value: "from_addr"}, {Key: "op", Value: "eq"}, {Key: "value", Value: "a@x.test"}}}}}
		return py.Dict{{Key: "rule", Value: rule}, {Key: "reason", Value: "operator_deleted"}, {Key: "messages", Value: snaps}}
	}, 1)
	if py.JSONDumps(p.Result) != `{"summary": "rule created, 1 moved to Trash, 0 skipped as changed", "rule": "del-x", "trashed": 1, "skipped": []}` {
		t.Fatalf("applied = %s %s", p.Status, py.JSONDumps(p.Result))
	}
	want := `{"op": "rule_create", "id": "del-x"} undo {"op": "rule_delete", "id": "del-x"}
{"op": "trash", "imap_name": "[Gmail]/All Mail", "uidvalidity": 1, "dest_imap": "[Gmail]/Trash", "items": [{"id": 1, "uid": 101, "dest_uid": 900}]} undo {"op": "gmail_untrash", "trash_imap": "[Gmail]/Trash", "dest_uidvalidity": 5, "uids": [900]}
`
	if got := actionsOf(t, d, p.ID); got != want {
		t.Fatalf("actions =\n%s\nwant\n%s", got, want)
	}
	r := proposals.NewRegistry()
	executors.Register(r, &executors.Executors{DB: d, Open: open, Now: func() float64 { return 3 }}, nil)
	r.Sync = nil
	out, err := (&proposals.Pipeline{DB: d, Registry: r, Now: func() float64 { return 3 }}).Undo(context.Background(), p.ID)
	must(t, err)
	if py.JSONDumps(out.Dict()) != `{"undone": true, "outcomes": [{"action": 2, "ok": true}, {"action": 1, "ok": true}], "sync_error": null}` {
		t.Fatalf("undo = %s", py.JSONDumps(out.Dict()))
	}
	var rules int
	must(t, d.Read.QueryRow("SELECT COUNT(*) FROM rules").Scan(&rules))
	if rules != 0 {
		t.Fatalf("rules after undo = %d", rules)
	}
}

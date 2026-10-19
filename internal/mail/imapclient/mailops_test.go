package imapclient

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/jeiang/atrium/internal/db/dbtest"
	"github.com/jeiang/atrium/internal/mail"
	maillive "github.com/jeiang/atrium/internal/mail/live"
	"github.com/jeiang/atrium/internal/mail/mailops"
	"github.com/jeiang/atrium/internal/proposals"
	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/safety"
)

func TestMoveUIDsWire(t *testing.T) {
	ctx := context.Background()
	client, _ := icloud(t, icloudLogin+`C: {t} SELECT INBOX
S: {t} OK [READ-WRITE] SELECT completed
C: {t} UID COPY 10:11 "Bills"
S: {t} OK [COPYUID 99 10:11 500:501] Done
C: {t} UID STORE 10:11 +FLAGS.SILENT (\Deleted)
S: {t} OK STORE completed
C: {t} UID EXPUNGE 10:11
S: * 3 EXPUNGE
S: * 3 EXPUNGE
S: {t} OK EXPUNGE completed
C: {t} SELECT INBOX
S: {t} OK [READ-WRITE] SELECT completed
C: {t} UID COPY 12 "Bills"
S: {t} OK [COPYUID 99 12 502] Done
C: {t} UID STORE 12 +FLAGS.SILENT (\Deleted)
S: {t} OK STORE completed
C: {t} UID EXPUNGE 12
S: {t} NO try later
`+logout, live)
	must(t, client.Connect(ctx))
	var got []*mail.CopyUID
	err := mailops.MoveUIDs(ctx, client, "INBOX", "Bills", []uint32{10, 11}, func(batch []uint32, copied *mail.CopyUID) error {
		got = append(got, copied)
		return nil
	})
	must(t, err)
	if len(got) != 1 || !reflect.DeepEqual(got[0], &mail.CopyUID{UIDValidity: 99, Mapping: map[uint32]uint32{10: 500, 11: 501}}) {
		t.Fatalf("moved = %+v", got)
	}
	err = mailops.MoveUIDs(ctx, client, "INBOX", "Bills", []uint32{12}, func([]uint32, *mail.CopyUID) error {
		t.Fatal("a batch that was not expunged was reported moved")
		return nil
	})
	var partial *mailops.PartialMove
	if !errors.As(err, &partial) || partial.Copied.Mapping[12] != 502 ||
		py.ExceptionText(err) != "PartialMove: copied but not expunged: IMAP command failed: NO [b'try later']" {
		t.Fatalf("partial move = %v", err)
	}
	client.Close()
}

func TestMoveUIDsWithoutUIDPLUSSendsNothing(t *testing.T) {
	ctx := context.Background()
	client, _ := icloud(t, `S: * OK [CAPABILITY IMAP4rev1] ready
C: {t} LOGIN "atrium.test@icloud.com" "app-specific password"
S: {t} OK [CAPABILITY IMAP4rev1 CONDSTORE] logged in
`+logout, live)
	must(t, client.Connect(ctx))
	err := mailops.MoveUIDs(ctx, client, "INBOX", "Bills", []uint32{1}, nil)
	if py.ExceptionText(err) != "MailOpError: server lacks UIDPLUS: refusing to move" {
		t.Fatalf("MoveUIDs = %v", err)
	}
	client.Close()
}

func TestVerifyFetchesHeadersOnlyWire(t *testing.T) {
	ctx := context.Background()
	client, _ := icloud(t, icloudLogin+`C/ ^{t} STATUS INBOX \((UIDVALIDITY|UIDNEXT|MESSAGES|HIGHESTMODSEQ)( (UIDVALIDITY|UIDNEXT|MESSAGES|HIGHESTMODSEQ)){3}\)$
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
S: * 2 FETCH (UID 302 FLAGS () BODY[HEADER] {24}
S: Message-ID: <z@x.test>
S: )
S: {t} OK FETCH completed
`+logout, live)
	must(t, client.Connect(ctx))
	d := dbtest.Schema(t)
	for _, q := range []string{
		"INSERT INTO accounts (name, kind) VALUES ('icloud', 'icloud')",
		"INSERT INTO mailboxes (id, account, name, imap_name, role) VALUES (1, 'icloud', 'INBOX', 'INBOX', 'inbox')",
		"INSERT INTO messages (id, account, mailbox_id, uidvalidity, uid, msgid, digest, first_seen) VALUES (1, 'icloud', 1, 7, 301, 'a@x.test', 'd1', 0)",
		"INSERT INTO messages (id, account, mailbox_id, uidvalidity, uid, msgid, digest, first_seen) VALUES (2, 'icloud', 1, 7, 302, 'b@x.test', 'd2', 0)",
	} {
		if _, err := d.Write.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	snaps, err := mailops.Snapshots(ctx, d.Read, []int64{1, 2})
	must(t, err)
	ok, skipped, err := mailops.Verify(ctx, d.Read, client, "icloud", snaps)
	must(t, err)
	if len(ok) != 1 || ok[0].ID != 1 || !reflect.DeepEqual(ok[0].Flags, []string{`\Seen`}) ||
		!reflect.DeepEqual(skipped, []mailops.Skip{{ID: 2, Reason: mailops.ReasonMsgID}}) {
		t.Fatalf("Verify = %+v, %+v", ok, skipped)
	}
	client.Close()
}

func TestUndoMoveBackWire(t *testing.T) {
	client, _ := icloud(t, icloudLogin+`C/ ^{t} STATUS "Bills" \((UIDVALIDITY|UIDNEXT|MESSAGES|HIGHESTMODSEQ)( (UIDVALIDITY|UIDNEXT|MESSAGES|HIGHESTMODSEQ)){3}\)$
S: * STATUS "Bills" (MESSAGES 3 UIDNEXT 504 UIDVALIDITY 99 HIGHESTMODSEQ 9)
S: {t} OK STATUS completed
C: {t} EXAMINE "Bills"
S: {t} OK [READ-ONLY] EXAMINE completed
C: {t} UID SEARCH HEADER "Message-ID" "m3@x.test"
S: * SEARCH 503
S: {t} OK SEARCH completed
C: {t} UID SEARCH UID 500:501,503
S: * SEARCH 500 503
S: {t} OK SEARCH completed
C: {t} SELECT "Bills"
S: {t} OK [READ-WRITE] SELECT completed
C: {t} UID COPY 500,503 INBOX
S: {t} OK [COPYUID 7 500,503 40:41] Done
C: {t} UID STORE 500,503 +FLAGS.SILENT (\Deleted)
S: {t} OK STORE completed
C: {t} UID EXPUNGE 500,503
S: * 1 EXPUNGE
S: * 2 EXPUNGE
S: {t} OK EXPUNGE completed
`+logout, shadow)
	r := proposals.NewRegistry()
	maillive.RegisterUndoers(r, func(ctx context.Context, _ string) (mail.Client, error) { return client, client.Connect(ctx) })
	plan := py.Dict{
		{Key: "op", Value: maillive.OpMoveBack}, {Key: "account", Value: "icloud"}, {Key: "src_imap", Value: "INBOX"},
		{Key: "dest_imap", Value: "Bills"}, {Key: "dest_uidvalidity", Value: 99},
		{Key: "items", Value: []any{
			py.Dict{{Key: "id", Value: 1}, {Key: "msgid", Value: "m1@x.test"}, {Key: "uid", Value: 10}, {Key: "dest_uid", Value: 500}},
			py.Dict{{Key: "id", Value: 2}, {Key: "msgid", Value: "m2@x.test"}, {Key: "uid", Value: 11}, {Key: "dest_uid", Value: 501}},
			py.Dict{{Key: "id", Value: 3}, {Key: "msgid", Value: "m3@x.test"}, {Key: "uid", Value: 12}, {Key: "dest_uid", Value: nil}},
		}},
	}
	ctx := safety.ApprovedWrites(context.Background(), "undo-1")
	detail, err := r.Undoers[maillive.OpMoveBack](ctx, []byte(py.JSONDumps(plan)))
	must(t, err)
	if !reflect.DeepEqual(detail, py.Dict{{Key: "restored", Value: 2}, {Key: "missing", Value: 1}}) {
		t.Fatalf("detail = %v", detail)
	}
}

func TestUndoUnflagRefusedOutsideApprovalSendsNothing(t *testing.T) {
	client, _ := icloud(t, icloudLogin+`C/ ^{t} STATUS INBOX \((UIDVALIDITY|UIDNEXT|MESSAGES|HIGHESTMODSEQ)( (UIDVALIDITY|UIDNEXT|MESSAGES|HIGHESTMODSEQ)){3}\)$
S: * STATUS INBOX (MESSAGES 3 UIDNEXT 304 UIDVALIDITY 7 HIGHESTMODSEQ 5)
S: {t} OK STATUS completed
`+logout, shadow)
	r := proposals.NewRegistry()
	maillive.RegisterUndoers(r, func(ctx context.Context, _ string) (mail.Client, error) { return client, client.Connect(ctx) })
	plan := `{"op": "unflag", "account": "icloud", "imap_name": "INBOX", "uidvalidity": 7, "uids": [303]}`
	_, err := r.Undoers[maillive.OpUnflag](context.Background(), []byte(plan))
	var refused safety.WriteRefused
	if !errors.As(err, &refused) {
		t.Fatalf("unapproved unflag = %v", err)
	}
}

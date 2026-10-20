package imapclient

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/db/dbtest"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/applier"
	maillive "github.com/jeiang/atrium/internal/mail/live"
	"github.com/jeiang/atrium/internal/safety"
	"github.com/jeiang/atrium/internal/testutil/imapscript"
)

const appliedList = `C: {t} LIST "" "*"
S: * LIST (\HasNoChildren) "/" INBOX
S: * LIST (\HasNoChildren) "/" "Bills"
S: * LIST (\HasNoChildren) "/" "Misc"
S: * LIST (\HasNoChildren \Trash) "/" "Deleted Messages"
S: {t} OK LIST completed
`

// pausing pauses atrium as soon as its first COPY has been sent.
type pausing struct {
	mail.Client
	pause string
}

func (p pausing) Copy(ctx context.Context, uids mail.UIDSet, dest string) (*mail.CopyUID, error) {
	copied, err := p.Client.Copy(ctx, uids, dest)
	if serr := safety.SetPaused(p.pause, true); serr != nil {
		return nil, serr
	}
	return copied, err
}

// wireApplier is an applier over a database holding one decided Inbox message per (uid, dest) pair, on an
// account whose client is the scripted session.
func wireApplier(t *testing.T, account string, client mail.Client, wrap func(mail.Client, config.Config) mail.Client, msgs ...[2]any) (*applier.Applier, *db.DB, *[]string) {
	t.Helper()
	dir := t.TempDir()
	cfg, err := config.Load(config.Map(map[string]string{
		"ATRIUM_MODE": "live", "ATRIUM_DB": filepath.Join(dir, "atrium.db"), "ATRIUM_CACHE_DIR": filepath.Join(dir, "cache"),
	}))
	must(t, err)
	d := dbtest.Schema(t)
	box, labels := "INBOX", "'[]'"
	if account == config.Gmail {
		box, labels = config.GmailAllMail, `'["\\Inbox"]'`
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := d.Write.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec("INSERT INTO accounts (name, kind) VALUES (?, ?)", account, account)
	exec("INSERT INTO mailboxes (id, account, name, imap_name, role) VALUES (1, ?, ?, ?, 'inbox')", account, box, box)
	for i, m := range msgs {
		id := i + 1
		exec("INSERT INTO messages (id, account, mailbox_id, uidvalidity, uid, msgid, digest, first_seen, in_inbox, seen, labels) "+
			"VALUES (?, ?, 1, 7, ?, ?, ?, 0, 1, 1, "+labels+")", id, account, m[0], fmt.Sprintf("m%d@x.test", id), fmt.Sprintf("d%d", id))
		exec("INSERT INTO decisions (message_id, account, ts, stage, action, dest) VALUES (?, ?, 1, 'learned', 'file', ?)", id, account, m[1])
	}
	var resynced []string
	l := &maillive.Live{DB: d, Cfg: cfg, Now: func() float64 { return 1_792_080_000 }}
	c := client
	if wrap != nil {
		c = wrap(client, cfg)
	}
	a := &applier.Applier{
		Live: l,
		Open: func(ctx context.Context, _ string) (mail.Client, error) { return c, client.Connect(ctx) },
		Resync: func(_ context.Context, account string) error {
			resynced = append(resynced, account)
			return nil
		},
	}
	return a, d, &resynced
}

func actionRows(t *testing.T, d *db.DB) []string {
	t.Helper()
	rows, err := d.Read.Query("SELECT COALESCE(ok, 'None'), COALESCE(error, ''), COALESCE(after, ''), COALESCE(undo, '') FROM auto_actions ORDER BY id")
	must(t, err)
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var ok, e, after, undo string
		must(t, rows.Scan(&ok, &e, &after, &undo))
		out = append(out, ok+"|"+e+"|"+after+"|"+undo)
	}
	return out
}

func TestApplierFilesICloudMailWire(t *testing.T) {
	ctx := context.Background()
	client, _ := icloud(t, icloudLogin+`C/ ^{t} STATUS INBOX \((UIDVALIDITY|UIDNEXT|MESSAGES|HIGHESTMODSEQ)( (UIDVALIDITY|UIDNEXT|MESSAGES|HIGHESTMODSEQ)){3}\)$
S: * STATUS INBOX (MESSAGES 1 UIDNEXT 302 UIDVALIDITY 7 HIGHESTMODSEQ 5)
S: {t} OK STATUS completed
C: {t} EXAMINE INBOX
S: {t} OK [READ-ONLY] EXAMINE completed
C: {t} UID SEARCH UID 301
S: * SEARCH 301
S: {t} OK SEARCH completed
C/ ^{t} UID FETCH 301 \((UID|FLAGS|BODY\.PEEK\[HEADER\])( (UID|FLAGS|BODY\.PEEK\[HEADER\])){2}\)$
S: * 1 FETCH (UID 301 FLAGS (\Seen) BODY[HEADER] {25}
S: Message-ID: <m1@x.test>
S: )
S: {t} OK FETCH completed
`+appliedList+appliedList+`C: {t} SELECT INBOX
S: * OK [UIDVALIDITY 7] UIDs valid
S: {t} OK [READ-WRITE] SELECT completed
C: {t} UID COPY 301 "Bills"
S: {t} OK [COPYUID 99 301 500] Done
C: {t} UID STORE 301 +FLAGS.SILENT (\Deleted)
S: {t} OK STORE completed
C: {t} UID EXPUNGE 301
S: * 1 EXPUNGE
S: {t} OK EXPUNGE completed
`+logout, live)
	a, d, resynced := wireApplier(t, config.ICloud, client, nil, [2]any{301, "Bills"})
	out, err := a.ApplyAccount(ctx, config.ICloud)
	must(t, err)
	if out.Written != 1 || len(*resynced) != 1 {
		t.Fatalf("outcome %+v resynced %v", out, *resynced)
	}
	got := actionRows(t, d)
	want := `1||{"imap_name": "Bills", "uidvalidity": 99, "uid": 500}|{"op": "move_back", "account": "icloud", "src_imap": "INBOX", ` +
		`"dest_imap": "Bills", "dest_uidvalidity": 99, "items": [{"id": 1, "msgid": "m1@x.test", "uid": 301, "dest_uid": 500}]}`
	if len(got) != 1 || got[0] != want {
		t.Fatalf("auto_actions = %q", got)
	}
}

func TestApplierFilesICloudMailVanishedWire(t *testing.T) {
	ctx := context.Background()
	client, _ := icloud(t, icloudLogin+`C/ ^{t} STATUS INBOX \((UIDVALIDITY|UIDNEXT|MESSAGES|HIGHESTMODSEQ)( (UIDVALIDITY|UIDNEXT|MESSAGES|HIGHESTMODSEQ)){3}\)$
S: * STATUS INBOX (MESSAGES 1 UIDNEXT 302 UIDVALIDITY 7 HIGHESTMODSEQ 5)
S: {t} OK STATUS completed
C: {t} EXAMINE INBOX
S: {t} OK [READ-ONLY] EXAMINE completed
C: {t} UID SEARCH UID 301
S: * SEARCH 301
S: {t} OK SEARCH completed
C/ ^{t} UID FETCH 301 \((UID|FLAGS|BODY\.PEEK\[HEADER\])( (UID|FLAGS|BODY\.PEEK\[HEADER\])){2}\)$
S: * 1 FETCH (UID 301 FLAGS (\Seen) BODY[HEADER] {25}
S: Message-ID: <m1@x.test>
S: )
S: {t} OK FETCH completed
`+appliedList+appliedList+`C: {t} SELECT INBOX
S: * OK [UIDVALIDITY 7] UIDs valid
S: {t} OK [READ-WRITE] SELECT completed
C: {t} UID COPY 301 "Bills"
S: {t} OK [COPYUID 99 301 500] Done
C: {t} UID STORE 301 +FLAGS.SILENT (\Deleted)
S: {t} OK STORE completed
C: {t} UID EXPUNGE 301
S: * VANISHED 301
S: * VANISHED (EARLIER) 1:4294967295
S: * VANISHED 1:4000000000
S: {t} OK EXPUNGE completed
`+logout, live)
	a, d, resynced := wireApplier(t, config.ICloud, client, nil, [2]any{301, "Bills"})
	out, err := a.ApplyAccount(ctx, config.ICloud)
	must(t, err)
	if out.Written != 1 || len(*resynced) != 1 {
		t.Fatalf("outcome %+v resynced %v", out, *resynced)
	}
	got := actionRows(t, d)
	want := `1||{"imap_name": "Bills", "uidvalidity": 99, "uid": 500}|{"op": "move_back", "account": "icloud", "src_imap": "INBOX", ` +
		`"dest_imap": "Bills", "dest_uidvalidity": 99, "items": [{"id": 1, "msgid": "m1@x.test", "uid": 301, "dest_uid": 500}]}`
	if len(got) != 1 || got[0] != want {
		t.Fatalf("auto_actions = %q", got)
	}
}

func TestApplierRelabelsGmailMailWire(t *testing.T) {
	ctx := context.Background()
	script := gmailLogin + `C/ ^{t} STATUS "\[Gmail\]/All Mail" \((UIDVALIDITY|UIDNEXT|MESSAGES|HIGHESTMODSEQ)( (UIDVALIDITY|UIDNEXT|MESSAGES|HIGHESTMODSEQ)){3}\)$
S: * STATUS "[Gmail]/All Mail" (MESSAGES 1 UIDNEXT 102 UIDVALIDITY 7 HIGHESTMODSEQ 9)
S: {t} OK Success
C: {t} EXAMINE "[Gmail]/All Mail"
S: {t} OK [READ-ONLY] [Gmail]/All Mail selected. (Success)
C: {t} UID SEARCH UID 101
S: * SEARCH 101
S: {t} OK SEARCH completed (Success)
C/ ^{t} UID FETCH 101 \((UID|FLAGS|BODY\.PEEK\[HEADER\]|X-GM-LABELS)( (UID|FLAGS|BODY\.PEEK\[HEADER\]|X-GM-LABELS)){3}\)$
S: * 1 FETCH (UID 101 X-GM-LABELS (\Inbox) FLAGS (\Seen) BODY[HEADER] {25}
S: Message-ID: <m1@x.test>
S: )
S: {t} OK Success
C: {t} LIST "" "*"
S: * LIST (\HasNoChildren) "/" "INBOX"
S: * LIST (\All \HasNoChildren) "/" "[Gmail]/All Mail"
S: * LIST (\HasNoChildren \Trash) "/" "[Gmail]/Trash"
S: {t} OK Success
C: {t} SELECT "[Gmail]/All Mail"
S: * OK [UIDVALIDITY 7] UIDs valid
S: {t} OK [READ-WRITE] [Gmail]/All Mail selected. (Success)
C: {t} UID STORE 101 +X-GM-LABELS ("Receipts")
S: * 1 FETCH (UID 101 X-GM-LABELS (\Inbox "Receipts"))
S: {t} OK Success
C: {t} UID STORE 101 -X-GM-LABELS (\Inbox)
S: * 1 FETCH (UID 101 X-GM-LABELS ("Receipts"))
S: {t} OK Success
` + logout
	client, _ := session(t, imapscript.MustParse(t, script), "atrium.test@gmail.com", "app password", live)
	a, d, _ := wireApplier(t, config.Gmail, client, nil, [2]any{101, "Receipts"})
	out, err := a.ApplyAccount(ctx, config.Gmail)
	must(t, err)
	if out.Written != 1 {
		t.Fatalf("outcome %+v", out)
	}
	got := actionRows(t, d)
	want := `1||{"add": ["Receipts"], "remove": ["\\Inbox"]}|{"op": "gmail_labels", "imap_name": "[Gmail]/All Mail", "uidvalidity": 7, ` +
		`"uids": [101], "add": ["\\Inbox"], "remove": ["Receipts"]}`
	if len(got) != 1 || got[0] != want {
		t.Fatalf("auto_actions = %q", got)
	}
}

// TestApplierPausedMidPassSendsNoFurtherWriteWire: once paused after the first COPY, the gate stops the STORE and
// EXPUNGE of that move and the whole second move before anything reaches the server.
func TestApplierPausedMidPassSendsNoFurtherWriteWire(t *testing.T) {
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
S: * 1 FETCH (UID 301 FLAGS (\Seen) BODY[HEADER] {25}
S: Message-ID: <m1@x.test>
S: )
S: * 2 FETCH (UID 302 FLAGS (\Seen) BODY[HEADER] {25}
S: Message-ID: <m2@x.test>
S: )
S: {t} OK FETCH completed
`+appliedList+appliedList+`C: {t} SELECT INBOX
S: * OK [UIDVALIDITY 7] UIDs valid
S: {t} OK [READ-WRITE] SELECT completed
C: {t} UID COPY 301 "Bills"
S: {t} OK [COPYUID 99 301 500] Done
`+logout, live)
	a, d, resynced := wireApplier(t, config.ICloud, client, func(c mail.Client, cfg config.Config) mail.Client {
		return pausing{c, cfg.PausePath()}
	}, [2]any{301, "Bills"}, [2]any{302, "Misc"})
	out, err := a.ApplyAccount(ctx, config.ICloud)
	must(t, err)
	if out.Written != 0 || out.Failed != 2 || len(*resynced) != 0 {
		t.Fatalf("outcome %+v resynced %v", out, *resynced)
	}
	got := actionRows(t, d)
	want := []string{
		`None|copied but not expunged: refusing automatic mailbox write 'IMAP STORE': atrium is paused|{"imap_name": "Bills", "uidvalidity": 99, "uid": 500}|`,
		`0|WriteRefused: refusing automatic mailbox write 'IMAP SELECT read-write': atrium is paused||`,
	}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("auto_actions = %q", got)
	}
}

package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/proposals"
	"github.com/jeiang/atrium/internal/proposals/executors"
	"github.com/jeiang/atrium/internal/settings"
	"github.com/jeiang/atrium/internal/testutil/imapscript"
	"github.com/jeiang/atrium/internal/testutil/llmstub"
)

func TestMailTriageProposalArchivesThroughTheCLI(t *testing.T) {
	dir, _, f := moduleFixture(t, flagsExcept("", ""))
	srv := llmstub.Server(t, nil)
	setEnv(t, "ATRIUM_MAIL_MAIL_CHAT_URL", srv.URL+"/v1")
	setEnv(t, "ATRIUM_MAIL_MAIL_EMBED_URL", srv.URL+"/v1")
	setEnv(t, settingEnv(mail.KeyMode), "live")
	setEnv(t, settingEnv(config.KeyMailAccountsFile), accountsFile(t, dir, mailAccount{"icloud", "icloud", "atrium.test@icloud.com", "app-pass", false}))

	d, err := db.Connect(filepath.Join(dir, "atrium.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(context.Background(), d, filepath.Join(dir, "atrium.db")); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.Exec("INSERT INTO accounts (name, kind) VALUES ('icloud', 'icloud')"); err != nil {
		t.Fatal(err)
	}
	res, err := d.Write.Exec("INSERT INTO mailboxes (account, name, imap_name, role, uidvalidity, highest_uid) VALUES ('icloud', 'INBOX', 'INBOX', 'inbox', 1544650202, 301)")
	if err != nil {
		t.Fatal(err)
	}
	mailboxID, _ := res.LastInsertId()
	res, err = d.Write.Exec("INSERT INTO messages (account, mailbox_id, uidvalidity, uid, msgid, digest, first_seen) VALUES ('icloud', ?, 1544650202, 301, 'm@x', 'd', 0)", mailboxID)
	if err != nil {
		t.Fatal(err)
	}
	messageID, _ := res.LastInsertId()
	account := "icloud"
	payload := map[string]any{"items": []any{map[string]any{
		"id": messageID, "uid": 301, "uidvalidity": 1544650202,
		"msgid": "m@x", "digest": "d", "mailbox": "INBOX",
		"imap_name": "INBOX", "action": "archive",
	}}}
	id, _, err := proposals.Create(settings.With(context.Background(), f.settings), d, executors.MailTriage, &account, "Triage",
		payload, map[string]any{}, "triage-archive-1", 0)
	if err != nil {
		t.Fatal(err)
	}

	script := imapscript.MustParse(t, `
S: {icloud.greeting}
C: {t} LOGIN "atrium.test@icloud.com" "app-pass"
S: {t} OK [CAPABILITY {icloud.authcaps}] User logged in
C/ ^{t} STATUS INBOX \(.*\)$
S: * STATUS "INBOX" (MESSAGES 1 UIDVALIDITY 1544650202 UIDNEXT 302 HIGHESTMODSEQ 81250)
S: {t} OK STATUS completed
C: {t} EXAMINE INBOX (CONDSTORE)
S: * FLAGS (\Answered \Flagged \Draft \Deleted \Seen)
S: * 1 EXISTS
S: * 0 RECENT
S: * OK [UIDVALIDITY 1544650202] UIDs valid
S: * OK [UIDNEXT 302] Predicted next UID
S: * OK [HIGHESTMODSEQ 81250] Highest
S: {t} OK [READ-ONLY] EXAMINE completed
C: {t} UID SEARCH UID 301
S: * SEARCH 301
S: {t} OK SEARCH completed
C/ ^{t} UID FETCH 301 \(.*\)$
S: * 1 FETCH (UID 301 FLAGS () BODY[HEADER] {17}
S: Message-ID: <m@x>)
S: {t} OK FETCH completed
C: {t} LIST "" "*"
S: * LIST (\Noselect) "/" "/"
S: {t} OK LIST completed
C: {t} LIST "" "*"
S: * LIST (\Noselect) "/" "/"
S: {t} OK LIST completed
C: {t} CREATE "Archive"
S: {t} OK CREATE completed
C: {t} SUBSCRIBE "Archive"
S: {t} OK SUBSCRIBE completed
C: {t} SELECT INBOX (CONDSTORE)
S: * FLAGS (\Answered \Flagged \Draft \Deleted \Seen)
S: * OK [PERMANENTFLAGS (\Answered \Flagged \Draft \Deleted \Seen \*)] Limited
S: * 1 EXISTS
S: * 0 RECENT
S: * OK [UIDVALIDITY 1544650202] UIDs valid
S: * OK [UIDNEXT 302] Predicted next UID
S: * OK [HIGHESTMODSEQ 81250] Highest
S: {t} OK [READ-WRITE] SELECT completed
C: {t} UID COPY 301 "Archive"
S: {t} OK [COPYUID 1544650202 301 900] COPY completed
C: {t} UID STORE 301 +FLAGS.SILENT (\Deleted)
S: {t} OK STORE completed
C: {t} UID EXPUNGE 301
S: * 1 EXPUNGE
S: {t} OK EXPUNGE completed
C: {t} LOGOUT
S: * BYE
S: {t} OK
! close
! accept
! close
`)
	server, ca := imapscript.ServeTLS(t, script)
	t.Setenv("ATRIUM_IMAP_ADDR", server.Addr())
	t.Setenv("ATRIUM_IMAP_CA", ca)

	var out, errs bytes.Buffer
	code := f.app.Run(context.Background(), []string{"proposals", "approve", strconv.FormatInt(id, 10)}, &out, &errs)
	stdout, stderr := out.String(), errs.String()
	if code != 0 {
		t.Fatalf("proposals approve: exit %d stdout %q stderr %q", code, stdout, stderr)
	}
	var status string
	if err := d.Read.QueryRow("SELECT status FROM proposals WHERE id = ?", id).Scan(&status); err != nil || status != "applied" {
		t.Fatalf("proposal status = %q, %v", status, err)
	}
	var action string
	if err := d.Read.QueryRow("SELECT action FROM proposal_actions WHERE proposal_id = ? AND kind = 'write' AND state = 'ok'", id).Scan(&action); err != nil {
		t.Fatalf("no recorded move: %v", err)
	}
	if !strings.Contains(action, `"op":"move"`) || !strings.Contains(action, `"dest_imap":"Archive"`) {
		t.Fatalf("recorded action = %s", action)
	}
}

package main

import (
	"context"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/testutil/imapscript"
	"github.com/jeiang/atrium/internal/testutil/llmstub"
)

type folderFixture struct {
	dir string
	api *modelLog
}

func newFolderFixture(t *testing.T, mode string) folderFixture {
	t.Helper()
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	srv := llmstub.Server(t, nil)
	setEnv(t, "ATRIUM_MAIL_MAIL_CHAT_URL", srv.URL+"/v1")
	seedSettings(t, map[string]string{mail.KeyMode: mode})
	setEnv(t, "ATRIUM_HOST_CONFIG", accountsFileEmbedding(t, dir, srv.URL+"/v1", filepath.Join(testRoot, "host", "anthropic.key"), mailAccount{"icloud", "icloud", "atrium.test@icloud.com", "app-pass", false}))
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
	res, err := d.Write.Exec("INSERT INTO mailboxes (account, name, imap_name, role, uidvalidity, highest_uid) VALUES ('icloud', 'INBOX', 'INBOX', 'inbox', 1544650202, 302)")
	if err != nil {
		t.Fatal(err)
	}
	mailboxID, _ := res.LastInsertId()
	for i, msg := range []struct{ uid, msgid, digest string }{{"301", "m1@x", "d1"}, {"302", "m2@x", "d2"}} {
		if _, err := d.Write.Exec("INSERT INTO messages (account, mailbox_id, uidvalidity, uid, msgid, digest, first_seen) VALUES ('icloud', ?, 1544650202, ?, ?, ?, 0)",
			mailboxID, msg.uid, msg.msgid, msg.digest); err != nil {
			t.Fatal(err)
		}
		_ = i
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if ran := runApp(t, "agent", "run", "--text", "warm"); ran.code != cli.ExitOK {
		t.Fatalf("warm run: %d %s", ran.code, ran.stderr)
	}
	return folderFixture{dir: dir, api: api}
}

const folderLogin = `S: {icloud.greeting}
C: {t} LOGIN "atrium.test@icloud.com" "app-pass"
S: {t} OK [CAPABILITY {icloud.authcaps}] User logged in
`

const folderClose = `C: {t} LOGOUT
S: * BYE
S: {t} OK
! close
`

func folderList(extra string) string {
	return `C: {t} LIST "" "*"
S: * LIST (\Noselect) "/" "/"
S: * LIST () "/" "INBOX"
` + extra + `S: {t} OK LIST completed
`
}

func TestMailFolderCreateAndMoveThroughTheAgent(t *testing.T) {
	fx := newFolderFixture(t, "live")
	script := folderLogin + folderList("") + folderList("") +
		`C: {t} CREATE "News"
S: {t} OK CREATE completed
C: {t} SUBSCRIBE "News"
S: {t} OK SUBSCRIBE completed
` + folderList(`S: * LIST () "/" "News"
`) + folderClose +
		`! accept
` + folderLogin + folderList(`S: * LIST () "/" "News"
`) +
		`C/ ^{t} STATUS INBOX \(.*\)$
S: * STATUS "INBOX" (MESSAGES 2 UIDVALIDITY 1544650202 UIDNEXT 303 HIGHESTMODSEQ 81250)
S: {t} OK STATUS completed
C: {t} EXAMINE INBOX (CONDSTORE)
S: * FLAGS (\Answered \Flagged \Draft \Deleted \Seen)
S: * 2 EXISTS
S: * 0 RECENT
S: * OK [UIDVALIDITY 1544650202] UIDs valid
S: * OK [UIDNEXT 303] Predicted next UID
S: * OK [HIGHESTMODSEQ 81250] Highest
S: {t} OK [READ-ONLY] EXAMINE completed
C: {t} UID SEARCH UID 301:302
S: * SEARCH 301 302
S: {t} OK SEARCH completed
C/ ^{t} UID FETCH 301:302 \(.*\)$
S: * 1 FETCH (UID 301 FLAGS () BODY[HEADER] {18}
S: Message-ID: <m1@x>)
S: * 2 FETCH (UID 302 FLAGS () BODY[HEADER] {18}
S: Message-ID: <m2@x>)
S: {t} OK FETCH completed
C: {t} SELECT INBOX (CONDSTORE)
S: * FLAGS (\Answered \Flagged \Draft \Deleted \Seen)
S: * OK [PERMANENTFLAGS (\Answered \Flagged \Draft \Deleted \Seen \*)] Limited
S: * 2 EXISTS
S: * 0 RECENT
S: * OK [UIDVALIDITY 1544650202] UIDs valid
S: * OK [UIDNEXT 303] Predicted next UID
S: * OK [HIGHESTMODSEQ 81250] Highest
S: {t} OK [READ-WRITE] SELECT completed
C: {t} UID COPY 301:302 "News"
S: {t} OK [COPYUID 1544650202 301:302 900:901] COPY completed
C: {t} UID STORE 301:302 +FLAGS.SILENT (\Deleted)
S: {t} OK STORE completed
C: {t} UID EXPUNGE 301:302
S: * 1 EXPUNGE
S: * 1 EXPUNGE
S: {t} OK EXPUNGE completed
` + folderClose +
		`! accept
` + folderLogin +
		`C/ ^{t} STATUS "News" \(.*\)$
S: * STATUS "News" (MESSAGES 2 UIDVALIDITY 1544650202 UIDNEXT 902 HIGHESTMODSEQ 1)
S: {t} OK STATUS completed
C: {t} EXAMINE "News" (CONDSTORE)
S: * FLAGS (\Answered \Flagged \Draft \Deleted \Seen)
S: * 2 EXISTS
S: * 0 RECENT
S: * OK [UIDVALIDITY 1544650202] UIDs valid
S: * OK [UIDNEXT 902] Predicted next UID
S: * OK [HIGHESTMODSEQ 1] Highest
S: {t} OK [READ-ONLY] EXAMINE completed
C: {t} UID SEARCH UID 900:901
S: * SEARCH 900 901
S: {t} OK SEARCH completed
C: {t} SELECT "News" (CONDSTORE)
S: * FLAGS (\Answered \Flagged \Draft \Deleted \Seen)
S: * OK [PERMANENTFLAGS (\Answered \Flagged \Draft \Deleted \Seen \*)] Limited
S: * 2 EXISTS
S: * 0 RECENT
S: * OK [UIDVALIDITY 1544650202] UIDs valid
S: * OK [UIDNEXT 902] Predicted next UID
S: * OK [HIGHESTMODSEQ 1] Highest
S: {t} OK [READ-WRITE] SELECT completed
C: {t} UID COPY 900:901 INBOX
S: {t} OK [COPYUID 1544650202 900:901 303:304] COPY completed
C: {t} UID STORE 900:901 +FLAGS.SILENT (\Deleted)
S: {t} OK STORE completed
C: {t} UID EXPUNGE 900:901
S: * 1 EXPUNGE
S: * 1 EXPUNGE
S: {t} OK EXPUNGE completed
` + folderClose +
		`! accept
` + folderLogin + folderList(`S: * LIST () "/" "News"
`) +
		`C/ ^{t} STATUS "News" \(.*\)$
S: * STATUS "News" (MESSAGES 0 UIDVALIDITY 1544650202 UIDNEXT 902 HIGHESTMODSEQ 1)
S: {t} OK STATUS completed
C: {t} DELETE "News"
S: {t} OK DELETE completed
` + folderClose

	server, ca := imapscript.ServeTLS(t, imapscript.MustParse(t, script))
	t.Setenv("ATRIUM_IMAP_ADDR", server.Addr())
	t.Setenv("ATRIUM_IMAP_CA", ca)

	fx.api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("f1", "mail_folder_create", map[string]any{"account": "icloud", "name": "News"})))
	fx.api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("f2", "mail_move", map[string]any{"account": "icloud", "folder": "News", "message_ids": []any{1, 2}})))
	fx.api.Push(anthropictest.Reply("end_turn", anthropictest.Text("done")))
	ran := runApp(t, "agent", "run", "--text", "create a news folder and file two messages")
	if ran.code != cli.ExitOK {
		t.Fatalf("agent run: %d %s", ran.code, ran.stderr)
	}

	d, err := db.Connect(filepath.Join(fx.dir, "atrium.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	rows, err := d.Read.Query("SELECT id, operation, state, after_json, undo_json FROM effects WHERE direction = 'forward' ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var ids []int64
	var tools, states, afters, undos []string
	for rows.Next() {
		var id int64
		var tool, state, after, undo string
		if err := rows.Scan(&id, &tool, &state, &after, &undo); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
		tools = append(tools, tool)
		states = append(states, state)
		afters = append(afters, after)
		undos = append(undos, undo)
	}
	if len(ids) != 2 || tools[0] != "mail_folder_create" || tools[1] != "mail_move" {
		var body string
		_ = d.Read.QueryRow("SELECT body FROM tool_results ORDER BY result_id DESC LIMIT 1").Scan(&body)
		t.Fatalf("writes = %v %v; last tool result = %s; stdout=%s stderr=%s", ids, tools, body, ran.stdout, ran.stderr)
	}
	if states[0] != "done" || states[1] != "done" {
		t.Fatalf("write states = %v", states)
	}
	if !strings.Contains(undos[0], `"op":"mail_folder_delete"`) || !strings.Contains(undos[0], `"imap_name":"News"`) {
		t.Fatalf("folder undo = %s", undos[0])
	}
	if !strings.Contains(undos[1], `"op":"mail_move_back"`) || !strings.Contains(undos[1], `"dest_uid":900`) || !strings.Contains(undos[1], `"dest_uid":901`) {
		t.Fatalf("move undo = %s", undos[1])
	}
	if !strings.Contains(afters[1], `"dest_imap":"News"`) {
		t.Fatalf("move after = %s", afters[1])
	}

	base := startServeIn(t, fx.dir)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if status, body, _ := fetch(t, client, "GET", base+"/activity", nil); status != http.StatusOK {
		t.Fatalf("GET /activity = %d %s", status, body)
	}
	for _, id := range []int64{ids[1], ids[0]} {
		status, body, _ := fetch(t, client, "POST", base+"/activity/effect/"+strconv.FormatInt(id, 10)+"/undo", url.Values{})
		if status != http.StatusSeeOther {
			t.Fatalf("undo write %d = %d %s", id, status, body)
		}
	}
	for _, id := range ids {
		var undone *int64
		if err := d.Read.QueryRow("SELECT undone_by FROM effects WHERE id = ?", id).Scan(&undone); err != nil {
			t.Fatal(err)
		}
		if undone == nil {
			t.Errorf("write %d was not marked undone", id)
		}
	}
}

func TestMailFolderCreateShadowRecordsWithoutWrite(t *testing.T) {
	fx := newFolderFixture(t, "shadow")
	t.Setenv("ATRIUM_IMAP_ADDR", "127.0.0.1:1")
	fx.api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("f1", "mail_folder_create", map[string]any{"account": "icloud", "name": "News"})))
	fx.api.Push(anthropictest.Reply("end_turn", anthropictest.Text("done")))
	ran := runApp(t, "agent", "run", "--text", "create a news folder")
	if ran.code != cli.ExitOK {
		t.Fatalf("agent run: %d %s", ran.code, ran.stderr)
	}
	d, err := db.Connect(filepath.Join(fx.dir, "atrium.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	var state, after, undo string
	if err := d.Read.QueryRow("SELECT state, after_json, coalesce(undo_json, '') FROM effects WHERE operation = 'mail_folder_create' AND direction = 'forward'").Scan(&state, &after, &undo); err != nil {
		t.Fatal(err)
	}
	if state != "skipped" || !strings.Contains(after, `"shadow":true`) || undo != "" {
		t.Fatalf("shadow write = %q %s %q", state, after, undo)
	}
}

func TestMailMoveRefusesProtectedFolder(t *testing.T) {
	fx := newFolderFixture(t, "live")
	fx.api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("f1", "mail_move", map[string]any{"account": "icloud", "folder": "Trash", "message_ids": []any{1}})))
	fx.api.Push(anthropictest.Reply("end_turn", anthropictest.Text("done")))
	ran := runApp(t, "agent", "run", "--text", "move one message to Trash")
	if ran.code != cli.ExitOK {
		t.Fatalf("agent run: %d %s", ran.code, ran.stderr)
	}
	d, err := db.Connect(filepath.Join(fx.dir, "atrium.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	var body string
	if err := d.Read.QueryRow("SELECT coalesce(error, '') FROM tool_calls ORDER BY id DESC LIMIT 1").Scan(&body); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "protected folder") {
		t.Fatalf("tool result = %s", body)
	}
	var n int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM effects WHERE direction = 'forward'").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("%d writes recorded for a protected folder", n)
	}
}

func TestTaskAddAcceptsANumericRemindAt(t *testing.T) {
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	if ran := runApp(t, "agent", "run", "--text", "warm"); ran.code != cli.ExitOK {
		t.Fatalf("warm run: %d %s", ran.code, ran.stderr)
	}
	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("t1", "task_add", map[string]any{"title": "call the bank", "remind_at": 1794150000})))
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("done")))
	ran := runApp(t, "agent", "run", "--text", "add a task with a reminder")
	if ran.code != cli.ExitOK {
		t.Fatalf("agent run: %d %s", ran.code, ran.stderr)
	}
	d, err := db.Connect(filepath.Join(dir, "atrium.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	var remind float64
	if err := d.Read.QueryRow("SELECT remind_at FROM tasks WHERE title = 'call the bank'").Scan(&remind); err != nil {
		t.Fatal(err)
	}
	if remind != 1794150000 {
		t.Fatalf("remind_at = %v", remind)
	}
}

func TestMailFolderUndoWorksInShadowMode(t *testing.T) {
	fx := newFolderFixture(t, "live")
	script := folderLogin + folderList("") + folderList("") +
		`C: {t} CREATE "News"
S: {t} OK CREATE completed
C: {t} SUBSCRIBE "News"
S: {t} OK SUBSCRIBE completed
` + folderList(`S: * LIST () "/" "News"
`) + folderClose +
		`! accept
` + folderLogin + folderList(`S: * LIST () "/" "News"
`) +
		`C/ ^{t} STATUS "News" \(.*\)$
S: * STATUS "News" (MESSAGES 0 UIDVALIDITY 1544650202 UIDNEXT 902 HIGHESTMODSEQ 1)
S: {t} OK STATUS completed
C: {t} DELETE "News"
S: {t} OK DELETE completed
` + folderClose
	server, ca := imapscript.ServeTLS(t, imapscript.MustParse(t, script))
	t.Setenv("ATRIUM_IMAP_ADDR", server.Addr())
	t.Setenv("ATRIUM_IMAP_CA", ca)

	fx.api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("f1", "mail_folder_create", map[string]any{"account": "icloud", "name": "News"})))
	fx.api.Push(anthropictest.Reply("end_turn", anthropictest.Text("done")))
	if ran := runApp(t, "agent", "run", "--text", "create a news folder"); ran.code != cli.ExitOK {
		t.Fatalf("agent run: %d %s", ran.code, ran.stderr)
	}
	d, err := db.Connect(filepath.Join(fx.dir, "atrium.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	var id int64
	if err := d.Read.QueryRow("SELECT id FROM effects WHERE operation = 'mail_folder_create' AND direction = 'forward'").Scan(&id); err != nil {
		t.Fatal(err)
	}
	seedSettings(t, map[string]string{mail.KeyMode: "shadow"})
	base := startServeIn(t, fx.dir)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if status, body, _ := fetch(t, client, "GET", base+"/activity", nil); status != http.StatusOK {
		t.Fatalf("GET /activity = %d %s", status, body)
	}
	if status, body, _ := fetch(t, client, "POST", base+"/activity/effect/"+strconv.FormatInt(id, 10)+"/undo", url.Values{}); status != http.StatusSeeOther {
		t.Fatalf("undo in shadow mode = %d %s", status, body)
	}
	var undone *int64
	if err := d.Read.QueryRow("SELECT undone_by FROM effects WHERE id = ?", id).Scan(&undone); err != nil {
		t.Fatal(err)
	}
	if undone == nil {
		t.Fatal("write was not marked undone")
	}
}

func TestMailMovePartialRecordsTheCopiedMessages(t *testing.T) {
	fx := newFolderFixture(t, "live")
	script := folderLogin + folderList(`S: * LIST () "/" "News"
`) +
		`C/ ^{t} STATUS INBOX \(.*\)$
S: * STATUS "INBOX" (MESSAGES 2 UIDVALIDITY 1544650202 UIDNEXT 303 HIGHESTMODSEQ 81250)
S: {t} OK STATUS completed
C: {t} EXAMINE INBOX (CONDSTORE)
S: * FLAGS (\Answered \Flagged \Draft \Deleted \Seen)
S: * 2 EXISTS
S: * 0 RECENT
S: * OK [UIDVALIDITY 1544650202] UIDs valid
S: * OK [UIDNEXT 303] Predicted next UID
S: * OK [HIGHESTMODSEQ 81250] Highest
S: {t} OK [READ-ONLY] EXAMINE completed
C: {t} UID SEARCH UID 301:302
S: * SEARCH 301 302
S: {t} OK SEARCH completed
C/ ^{t} UID FETCH 301:302 \(.*\)$
S: * 1 FETCH (UID 301 FLAGS () BODY[HEADER] {18}
S: Message-ID: <m1@x>)
S: * 2 FETCH (UID 302 FLAGS () BODY[HEADER] {18}
S: Message-ID: <m2@x>)
S: {t} OK FETCH completed
C: {t} SELECT INBOX (CONDSTORE)
S: * FLAGS (\Answered \Flagged \Draft \Deleted \Seen)
S: * OK [PERMANENTFLAGS (\Answered \Flagged \Draft \Deleted \Seen \*)] Limited
S: * 2 EXISTS
S: * 0 RECENT
S: * OK [UIDVALIDITY 1544650202] UIDs valid
S: * OK [UIDNEXT 303] Predicted next UID
S: * OK [HIGHESTMODSEQ 81250] Highest
S: {t} OK [READ-WRITE] SELECT completed
C: {t} UID COPY 301:302 "News"
S: {t} OK [COPYUID 1544650202 301:302 900:901] COPY completed
C: {t} UID STORE 301:302 +FLAGS.SILENT (\Deleted)
S: {t} NO STORE refused
` + folderClose
	server, ca := imapscript.ServeTLS(t, imapscript.MustParse(t, script))
	t.Setenv("ATRIUM_IMAP_ADDR", server.Addr())
	t.Setenv("ATRIUM_IMAP_CA", ca)

	fx.api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("f1", "mail_move", map[string]any{"account": "icloud", "folder": "News", "message_ids": []any{1, 2}})))
	fx.api.Push(anthropictest.Reply("end_turn", anthropictest.Text("done")))
	if ran := runApp(t, "agent", "run", "--text", "file the two messages"); ran.code != cli.ExitOK {
		t.Fatalf("agent run: %d %s", ran.code, ran.stderr)
	}
	d, err := db.Connect(filepath.Join(fx.dir, "atrium.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	var state, after, undo string
	if err := d.Read.QueryRow("SELECT state, after_json, undo_json FROM effects WHERE direction = 'forward' AND operation = 'mail_move' ORDER BY id DESC LIMIT 1").
		Scan(&state, &after, &undo); err != nil {
		t.Fatal(err)
	}
	if state != "partial" {
		t.Fatalf("move state = %q, want partial; after=%s undo=%s", state, after, undo)
	}
	if !strings.Contains(undo, `"op":"mail_move_back"`) || !strings.Contains(undo, `"dest_uid":900`) || !strings.Contains(undo, `"dest_uid":901`) {
		t.Fatalf("the partial move dropped its inverse: %s", undo)
	}
	if !strings.Contains(after, `"copied"`) && !strings.Contains(after, `"dest_uid":900`) {
		t.Fatalf("the partial move dropped the copied evidence: %s", after)
	}
}

func TestMailFolderCreateRecordsAPartialWhenSubscribeFails(t *testing.T) {
	fx := newFolderFixture(t, "live")
	script := folderLogin + folderList("") + folderList("") +
		`C: {t} CREATE "News"
S: {t} OK CREATE completed
C: {t} SUBSCRIBE "News"
S: {t} NO SUBSCRIBE refused
` + folderClose
	server, ca := imapscript.ServeTLS(t, imapscript.MustParse(t, script))
	t.Setenv("ATRIUM_IMAP_ADDR", server.Addr())
	t.Setenv("ATRIUM_IMAP_CA", ca)

	fx.api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("f1", "mail_folder_create", map[string]any{"account": "icloud", "name": "News"})))
	fx.api.Push(anthropictest.Reply("end_turn", anthropictest.Text("done")))
	if ran := runApp(t, "agent", "run", "--text", "create a news folder"); ran.code != cli.ExitOK {
		t.Fatalf("agent run: %d %s", ran.code, ran.stderr)
	}
	d, err := db.Connect(filepath.Join(fx.dir, "atrium.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	var state, undo string
	if err := d.Read.QueryRow("SELECT state, undo_json FROM effects WHERE direction = 'forward' AND operation = 'mail_folder_create' ORDER BY id DESC LIMIT 1").
		Scan(&state, &undo); err != nil {
		t.Fatal(err)
	}
	if state != "partial" {
		t.Fatalf("folder create state = %q, want partial; undo=%s", state, undo)
	}
	if !strings.Contains(undo, `"op":"mail_folder_delete"`) || !strings.Contains(undo, `"imap_name":"News"`) {
		t.Fatalf("the created folder has no delete inverse: %s", undo)
	}
}

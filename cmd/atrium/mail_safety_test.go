package main

import (
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/embed"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/mailtest"
	"github.com/jeiang/atrium/internal/testutil/llmstub"
)

type mailSafety struct {
	dir  string
	app  *cli.App
	api  *modelLog
	fake *mailtest.FakeClient
	path string
}

func mailSafetyFixture(t *testing.T, mode string) *mailSafety {
	t.Helper()
	dir, api, f := moduleFixture(t, flagsExcept("", ""))
	fake := mailtest.NewFakeClient()
	fake.AddBox("INBOX", mailtest.NewBox(1))
	fake.AddBox("Bills", mailtest.NewBox(3))
	fake.AddBox("Receipts", mailtest.NewBox(4))
	fake.AddBox("Deleted Messages", mailtest.NewBox(9), `\Trash`)
	fake.AddBox("Sent Messages", mailtest.NewBox(5), `\Sent`)
	f.app = newAppWith(f.lookup, f.settings, f.state, nil, func(config.Config) mail.OpenFunc { return fake.Open }, nil, quietLog)
	srv := llmstub.Server(t, nil)
	setEnv(t, "ATRIUM_MAIL_MAIL_CHAT_URL", srv.URL+"/v1")
	setEnv(t, "ATRIUM_MAIL_MAIL_EMBED_URL", "http://127.0.0.1:1/v1")
	setEnv(t, settingEnv(mail.KeyMode), mode)
	setEnv(t, settingEnv(config.KeyMailAccountsFile), accountsFile(t, dir, mailAccount{"icloud", "icloud", "atrium.test@icloud.com", "app-pass", false}))
	return &mailSafety{dir: dir, app: f.app, api: api, fake: fake, path: filepath.Join(dir, "atrium.db")}
}

func (m *mailSafety) add(box, subject, from, msgid, body string, flags ...string) uint32 {
	h, x := mailtest.RawWith(subject, from, msgid, body)
	return m.fake.Boxes[box].Box.Add(h, x, flags, nil)
}

func (m *mailSafety) run(t *testing.T, args ...string) appRun {
	t.Helper()
	ran := runApp(t, args...)
	if ran.code != cli.ExitOK {
		t.Fatalf("%v: exit %d stdout %q stderr %q", args, ran.code, ran.stdout, ran.stderr)
	}
	return ran
}

func (m *mailSafety) exec(t *testing.T, query string, args ...any) {
	t.Helper()
	d := mustConnect(t, m.path)
	if _, err := d.Write.Exec(query, args...); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
}

func (m *mailSafety) scalar(t *testing.T, query string, args ...any) string {
	t.Helper()
	d := mustConnect(t, m.path)
	var out *string
	if err := d.Read.QueryRow(query, args...).Scan(&out); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	if out == nil {
		return "<nil>"
	}
	return *out
}

func (m *mailSafety) messageID(t *testing.T, subject string) int64 {
	t.Helper()
	var id int64
	d := mustConnect(t, m.path)
	if err := d.Read.QueryRow("SELECT id FROM messages WHERE subject = ?", subject).Scan(&id); err != nil {
		t.Fatalf("message %q: %v", subject, err)
	}
	return id
}

func (m *mailSafety) decision(t *testing.T, messageID int64) (stage, action, dest string) {
	t.Helper()
	d := mustConnect(t, m.path)
	var target *string
	if err := d.Read.QueryRow("SELECT stage, action, dest FROM decisions WHERE message_id = ? ORDER BY id DESC LIMIT 1", messageID).
		Scan(&stage, &action, &target); err != nil {
		t.Fatalf("decision for message %d: %v", messageID, err)
	}
	if target != nil {
		dest = *target
	}
	return stage, action, dest
}

func (m *mailSafety) verbs() []string {
	out := make([]string, len(m.fake.Writes))
	for i, w := range m.fake.Writes {
		out[i] = w.Verb
	}
	return out
}

func (m *mailSafety) embed(t *testing.T, ids ...int64) {
	t.Helper()
	vector := make([]float64, 768)
	vector[0] = 1
	blob := embed.Serialize(vector)
	for _, id := range ids {
		m.exec(t, "INSERT INTO embed_message_0 (rowid, account, embedding) VALUES (?, 'icloud', ?)", id, blob)
		m.exec(t, "UPDATE messages SET embedded_hash = 'test' WHERE id = ?", id)
	}
}

func TestMailRoutingRecordsEachStage(t *testing.T) {
	const from = "statements@bank.test"
	type filedMail struct{ box, subject, from, msgid string }
	cases := []struct {
		stage   string
		subject string
		filed   []filedMail
		prepare func(t *testing.T, m *mailSafety, id int64)
		dest    string
	}{
		{
			stage: "correction",
			prepare: func(t *testing.T, m *mailSafety, id int64) {
				m.exec(t, "INSERT INTO corrections (message_id, account, folder, ts, source) VALUES (?, 'icloud', 'Bills', ?, 'operator')",
					id, mail.WallClock()+60)
			},
			dest: "Bills",
		},
		{
			stage: "operator",
			prepare: func(t *testing.T, m *mailSafety, _ int64) {
				path := filepath.Join(m.dir, "rules.json")
				body := `{"rules":[{"id":"bank","account":"icloud","action":"file","dest":"Bills",` +
					`"when":[{"field":"from_addr","op":"eq","value":"statements@bank.test"}]}]}`
				if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
				m.run(t, "rules", "import", path)
			},
			dest: "Bills",
		},
		{
			stage: "learned",
			filed: []filedMail{
				{"Bills", "Statement 1", from, "s1"},
				{"Bills", "Statement 2", from, "s2"},
				{"Bills", "Statement 3", from, "s3"},
			},
			dest: "Bills",
		},
		{
			stage: "knn",
			filed: []filedMail{{"Bills", "Statement 9", "other@bank.test", "s9"}},
			prepare: func(t *testing.T, m *mailSafety, id int64) {
				m.embed(t, id, m.messageID(t, "Statement 9"))
			},
			dest: "Bills",
		},
		{
			stage:   "llm",
			subject: "Order confirmation 55123",
			filed: []filedMail{
				{"Bills", "Statement 8", "bills@bank.test", "s8"},
				{"Receipts", "Receipt 7", "shop@store.test", "s7"},
			},
			prepare: func(t *testing.T, m *mailSafety, id int64) {
				m.embed(t, id, m.messageID(t, "Statement 8"), m.messageID(t, "Receipt 7"))
			},
			dest: "Receipts",
		},
		{
			stage: "fallback",
			prepare: func(t *testing.T, m *mailSafety, id int64) {
				m.exec(t, "UPDATE messages SET embedded_hash = 'test' WHERE id = ?", id)
			},
			dest: "Misc",
		},
	}
	for _, c := range cases {
		t.Run(c.stage, func(t *testing.T) {
			m := mailSafetyFixture(t, "shadow")
			setEnv(t, settingEnv(mail.KeyFolderMinMessages), "1")
			subject := c.subject
			if subject == "" {
				subject = "Bank statement"
			}
			uid := m.add("INBOX", subject, from, "target", "Your statement is ready.")
			for _, f := range c.filed {
				m.add(f.box, f.subject, f.from, f.msgid, "Filed message.")
			}
			m.run(t, "sync")
			id := m.messageID(t, subject)
			if c.prepare != nil {
				c.prepare(t, m, id)
			}
			m.fake.Boxes["INBOX"].Box.SetFlags(uid, []string{`\Seen`}, nil)
			m.run(t, "sync")

			stage, action, dest := m.decision(t, id)
			if stage != c.stage || action != "file" || dest != c.dest {
				t.Fatalf("decision = %s/%s/%s, want %s/file/%s", stage, action, dest, c.stage, c.dest)
			}
			if len(m.fake.Writes) != 0 {
				t.Fatalf("routing in shadow mode wrote to IMAP: %v", m.verbs())
			}
		})
	}
}

func (m *mailSafety) rule(t *testing.T, dest, from string) {
	t.Helper()
	path := filepath.Join(m.dir, "rules.json")
	body := `{"rules":[{"id":"bank","account":"icloud","action":"file","dest":"` + dest + `",` +
		`"when":[{"field":"from_addr","op":"eq","value":"` + from + `"}]}]}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	m.run(t, "rules", "import", path)
}

func (m *mailSafety) count(t *testing.T, query string, args ...any) int {
	t.Helper()
	d := mustConnect(t, m.path)
	var n int
	if err := d.Read.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

func (m *mailSafety) copies() int {
	var n int
	for _, w := range m.fake.Writes {
		if w.Verb == "COPY" {
			n++
		}
	}
	return n
}

func TestMailGateRefusalsWriteNothing(t *testing.T) {
	const from = "statements@bank.test"

	t.Run("shadow", func(t *testing.T) {
		m := mailSafetyFixture(t, "shadow")
		uid := m.add("INBOX", "Bank statement", from, "target", "Your statement is ready.")
		m.rule(t, "Bills", from)
		m.run(t, "sync")
		id := m.messageID(t, "Bank statement")
		m.fake.Boxes["INBOX"].Box.SetFlags(uid, []string{`\Seen`}, nil)
		m.run(t, "sync")

		if stage, _, dest := m.decision(t, id); stage != "operator" || dest != "Bills" {
			t.Fatalf("decision = %s/%s, want operator/Bills", stage, dest)
		}
		if got := m.verbs(); len(got) != 0 {
			t.Fatalf("shadow mode wrote to IMAP: %v", got)
		}
		if n := m.count(t, "SELECT COUNT(*) FROM auto_actions"); n != 0 {
			t.Fatalf("shadow mode recorded %d automatic actions", n)
		}
	})

	t.Run("paused", func(t *testing.T) {
		m := mailSafetyFixture(t, "live")
		uid := m.add("INBOX", "Bank statement", from, "target", "Your statement is ready.")
		m.run(t, "sync")
		id := m.messageID(t, "Bank statement")
		m.run(t, "live", "rebaseline")
		m.exec(t, "INSERT INTO corrections (message_id, account, folder, ts, source) VALUES (?, 'icloud', 'Bills', ?, 'operator')",
			id, mail.WallClock()+60)
		m.run(t, "live", "pause")
		m.fake.Boxes["INBOX"].Box.SetFlags(uid, []string{`\Seen`}, nil)
		m.run(t, "sync")

		if got := m.verbs(); len(got) != 0 {
			t.Fatalf("a paused pass wrote to IMAP: %v", got)
		}
		if n := m.count(t, "SELECT COUNT(*) FROM auto_actions"); n != 0 {
			t.Fatalf("a paused pass recorded %d automatic actions", n)
		}

		m.run(t, "live", "resume")
		m.run(t, "sync")
		if got := m.verbs(); len(got) != 3 || got[0] != "COPY" || got[1] != "STORE" || got[2] != "EXPUNGE" {
			t.Fatalf("after resume the write log = %v, want COPY, STORE, EXPUNGE", got)
		}
		if state := m.scalar(t, "SELECT state FROM auto_actions WHERE kind = 'correct'"); state != "applied" {
			t.Fatalf("correction state after resume = %s", state)
		}
	})

	t.Run("protected", func(t *testing.T) {
		m := mailSafetyFixture(t, "live")
		uid := m.add("INBOX", "Bank statement", from, "target", "Your statement is ready.")
		m.run(t, "sync")
		id := m.messageID(t, "Bank statement")
		m.run(t, "live", "rebaseline")
		m.exec(t, "INSERT INTO corrections (message_id, account, folder, ts, source) VALUES (?, 'icloud', 'Deleted Messages', ?, 'operator')",
			id, mail.WallClock()+60)
		m.fake.Boxes["INBOX"].Box.SetFlags(uid, []string{`\Seen`}, nil)
		m.run(t, "sync")

		if state := m.scalar(t, "SELECT state FROM auto_actions WHERE kind = 'correct'"); state != "skipped" {
			t.Fatalf("a correction into a protected folder recorded %s, want skipped", state)
		}
		if got := m.verbs(); len(got) != 0 {
			t.Fatalf("a correction into a protected folder wrote to IMAP: %v", got)
		}
		if n := len(m.fake.Boxes["Deleted Messages"].Box.Messages); n != 0 {
			t.Fatalf("the protected folder holds %d messages", n)
		}
	})

	t.Run("cap", func(t *testing.T) {
		m := mailSafetyFixture(t, "live")
		setEnv(t, settingEnv(mail.KeyWriteCap), "1")
		first := m.add("INBOX", "First statement", from, "first", "Your statement is ready.")
		second := m.add("INBOX", "Second statement", from, "second", "Another statement is ready.")
		m.run(t, "sync")
		firstID := m.messageID(t, "First statement")
		secondID := m.messageID(t, "Second statement")
		m.run(t, "live", "rebaseline")
		for _, id := range []int64{firstID, secondID} {
			m.exec(t, "INSERT INTO corrections (message_id, account, folder, ts, source) VALUES (?, 'icloud', 'Bills', ?, 'operator')",
				id, mail.WallClock()+60)
		}
		m.fake.Boxes["INBOX"].Box.SetFlags(first, []string{`\Seen`}, nil)
		m.fake.Boxes["INBOX"].Box.SetFlags(second, []string{`\Seen`}, nil)
		m.run(t, "sync")

		if n := m.copies(); n != 1 {
			t.Fatalf("with a cap of one the pass made %d copies, want 1", n)
		}
		if n := m.count(t, "SELECT COUNT(*) FROM auto_actions WHERE state = 'applied'"); n != 1 {
			t.Fatalf("with a cap of one %d actions applied, want 1", n)
		}
		if n := m.count(t, "SELECT COUNT(*) FROM auto_actions WHERE message_id = ?", secondID); n != 0 {
			t.Fatalf("the deferred message has %d automatic actions", n)
		}

		setEnv(t, settingEnv(mail.KeyWriteCap), "2")
		m.run(t, "sync")
		if n := m.copies(); n != 2 {
			t.Fatalf("after raising the cap the pass made %d copies, want 2", n)
		}
		if n := m.count(t, "SELECT COUNT(*) FROM auto_actions WHERE kind = 'correct' AND state = 'applied'"); n != 2 {
			t.Fatalf("%d corrections applied after raising the cap, want 2", n)
		}
	})
}

func TestMailLiveUndoRestoresTheMessageAndAnUnknownUndoStaysBlocked(t *testing.T) {
	const from = "statements@bank.test"
	prepare := func(t *testing.T) (*mailSafety, string) {
		t.Helper()
		m := mailSafetyFixture(t, "live")
		uid := m.add("INBOX", "Bank statement", from, "target", "Your statement is ready.")
		m.run(t, "sync")
		id := m.messageID(t, "Bank statement")
		m.run(t, "live", "rebaseline")
		m.exec(t, "INSERT INTO corrections (message_id, account, folder, ts, source) VALUES (?, 'icloud', 'Bills', ?, 'operator')",
			id, mail.WallClock()+60)
		m.fake.Boxes["INBOX"].Box.SetFlags(uid, []string{`\Seen`}, nil)
		m.run(t, "sync")
		action := m.scalar(t, "SELECT CAST(id AS TEXT) FROM auto_actions WHERE kind = 'correct'")
		if state := m.scalar(t, "SELECT state FROM auto_actions WHERE id = ?", action); state != "applied" {
			t.Fatalf("forward correction state = %s, want applied", state)
		}
		if got := m.verbs(); len(got) != 3 || got[0] != "COPY" || got[1] != "STORE" || got[2] != "EXPUNGE" {
			t.Fatalf("the forward move wrote %v, want COPY, STORE, EXPUNGE and never MOVE", got)
		}
		if n := len(m.fake.Boxes["INBOX"].Box.Messages); n != 0 {
			t.Fatalf("the inbox holds %d messages after the move, want 0", n)
		}
		if n := len(m.fake.Boxes["Bills"].Box.Messages); n != 1 {
			t.Fatalf("the destination holds %d messages after the move, want 1", n)
		}
		return m, action
	}

	t.Run("undo", func(t *testing.T) {
		m, action := prepare(t)
		m.run(t, "live", "undo", action)
		if state := m.scalar(t, "SELECT state FROM auto_actions WHERE id = ?", action); state != "undone" {
			t.Fatalf("the action after undo is %s, want undone", state)
		}
		if n := len(m.fake.Boxes["INBOX"].Box.Messages); n != 1 {
			t.Fatalf("the inbox holds %d messages after the undo, want 1", n)
		}
		if n := len(m.fake.Boxes["Bills"].Box.Messages); n != 0 {
			t.Fatalf("the destination holds %d messages after the undo, want 0", n)
		}
		if got := m.verbs(); len(got) != 6 || got[3] != "COPY" || got[4] != "STORE" || got[5] != "EXPUNGE" {
			t.Fatalf("the undo wrote %v, want COPY, STORE, EXPUNGE", got)
		}
		if ran := runApp(t, "live", "undo", action); ran.code == cli.ExitOK {
			t.Fatalf("a second undo exited %d", ran.code)
		}
	})

	t.Run("unknown", func(t *testing.T) {
		m, action := prepare(t)
		m.fake.FailWrite = func(w mailtest.FakeWrite) error {
			if w.Verb == "COPY" {
				return &mail.WriteInterrupted{Command: "UID COPY", Err: errors.New("connection reset by peer")}
			}
			return nil
		}
		if ran := runApp(t, "live", "undo", action); ran.code == cli.ExitOK {
			t.Fatalf("an undo with a dropped connection exited %d", ran.code)
		}
		if state := m.scalar(t, "SELECT state FROM auto_actions WHERE id = ?", action); state != "undo_unknown" {
			t.Fatalf("the undo after a dropped connection is %s, want undo_unknown", state)
		}
		if ran := runApp(t, "live", "undo", action); ran.code == cli.ExitOK {
			t.Fatalf("a second undo of a blocked undo exited %d", ran.code)
		}
		if n := len(m.fake.Boxes["Bills"].Box.Messages); n != 1 {
			t.Fatalf("the destination holds %d messages, want the blocked undo left alone", n)
		}
		m.run(t, "live", "resolve", action, "--outcome", "undone")
		if state := m.scalar(t, "SELECT state FROM auto_actions WHERE id = ?", action); state != "undone" {
			t.Fatalf("after the operator resolution the action is %s, want undone", state)
		}
	})
}

func TestMailUnknownForwardOutcomeIsNotRetried(t *testing.T) {
	m := mailSafetyFixture(t, "live")
	uid := m.add("INBOX", "Bank statement", "statements@bank.test", "target", "Your statement is ready.")
	m.run(t, "sync")
	id := m.messageID(t, "Bank statement")
	m.run(t, "live", "rebaseline")
	m.exec(t, "INSERT INTO corrections (message_id, account, folder, ts, source) VALUES (?, 'icloud', 'Bills', ?, 'operator')",
		id, mail.WallClock()+60)
	m.fake.Boxes["INBOX"].Box.SetFlags(uid, []string{`\Seen`}, nil)

	copies := 0
	m.fake.FailWrite = func(w mailtest.FakeWrite) error {
		if w.Verb == "COPY" {
			copies++
			return &mail.WriteInterrupted{Command: "UID COPY", Err: errors.New("connection reset by peer")}
		}
		return nil
	}
	if ran := runApp(t, "sync"); ran.code == cli.ExitOK {
		t.Fatalf("a pass whose only move had an unknown outcome exited 0")
	}

	action := m.scalar(t, "SELECT CAST(id AS TEXT) FROM auto_actions WHERE kind = 'correct'")
	if state := m.scalar(t, "SELECT state FROM auto_actions WHERE id = ?", action); state == "applied" {
		t.Fatalf("a move whose copy was dropped is recorded as applied")
	}
	if recorded := m.scalar(t, "SELECT CAST(error IS NOT NULL AS TEXT) FROM auto_actions WHERE id = ?", action); recorded != "1" {
		t.Fatalf("the blocked move recorded no error text")
	}
	if copies != 1 {
		t.Fatalf("the unknown move made %d copy attempts, want 1", copies)
	}
	if n := len(m.fake.Boxes["INBOX"].Box.Messages); n != 1 {
		t.Fatalf("the inbox holds %d messages after the dropped copy, want 1", n)
	}
	if n := len(m.fake.Boxes["Bills"].Box.Messages); n != 0 {
		t.Fatalf("the destination holds %d messages, want 0", n)
	}
	attempts := len(m.fake.Writes)

	m.fake.FailWrite = nil
	m.run(t, "sync")
	if got := len(m.fake.Writes); got != attempts {
		t.Fatalf("the next pass made %d writes, want the blocked move left alone", got-attempts)
	}
	if state := m.scalar(t, "SELECT state FROM auto_actions WHERE id = ?", action); state == "applied" {
		t.Fatalf("the next pass applied the blocked move")
	}

	base, _, _ := startServeApp(t, m.app, &lockedBuffer{})
	client := mrvClient(t)
	if code, _, _ := mrvDo(t, client, "GET", base+"/triage", nil); code != 200 {
		t.Fatalf("GET /triage = %d", code)
	}
	code, body, _ := mrvDo(t, client, "POST", base+"/messages/"+m.scalar(t, "SELECT CAST(id AS TEXT) FROM messages WHERE id = ?", id)+"/correct",
		url.Values{"dest": {"Bills"}, "context": {"row"}}, "HX-Request", "true")
	if code != 200 {
		t.Fatalf("the correction pass = %d: %.400s", code, body)
	}
	if got := len(m.fake.Writes); got != attempts {
		t.Fatalf("the correction pass made %d writes, want the blocked move left alone", got-attempts)
	}
	if state := m.scalar(t, "SELECT state FROM auto_actions WHERE id = ?", action); state == "applied" {
		t.Fatalf("the correction pass applied the blocked move")
	}
}

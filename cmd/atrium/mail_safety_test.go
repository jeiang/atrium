package main

import (
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/contacts"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/embed"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/mailtest"
	"github.com/jeiang/atrium/internal/testutil/mailscript"
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
	f.app = newAppWith(f.lookup, func(config.Config) mail.OpenFunc { return fake.Open }, nil, quietLog)
	f.settings, f.state = f.app.Settings, f.app.State
	api.AnswerJSON(mailscript.Load(t).JSONAnswer)
	seedSettings(t, map[string]string{mail.KeyMode: mode})
	setEnv(t, "ATRIUM_HOST_CONFIG", accountsFileEmbedding(t, dir, "http://127.0.0.1:1/v1", filepath.Join(testRoot, "host", "anthropic.key"), mailAccount{"icloud", "icloud", "atrium.test@icloud.com", "app-pass", false}))
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
	d := mustConnect(t, m.path, db.Key{})
	if _, err := d.Write.Exec(query, args...); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
}

func (m *mailSafety) scalar(t *testing.T, query string, args ...any) string {
	t.Helper()
	d := mustConnect(t, m.path, db.Key{})
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
	d := mustConnect(t, m.path, db.Key{})
	if err := d.Read.QueryRow("SELECT id FROM messages WHERE subject = ?", subject).Scan(&id); err != nil {
		t.Fatalf("message %q: %v", subject, err)
	}
	return id
}

func (m *mailSafety) decision(t *testing.T, messageID int64) (stage, action, dest string) {
	t.Helper()
	d := mustConnect(t, m.path, db.Key{})
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
				{"Bills", "Filed 1", "f1@filler1.test", "f1"},
				{"Bills", "Filed 2", "f2@filler2.test", "f2"},
				{"Bills", "Filed 3", "f3@filler3.test", "f3"},
				{"Bills", "Filed 4", "f4@filler4.test", "f4"},
				{"Bills", "Filed 5", "f5@filler5.test", "f5"},
				{"Bills", "Filed 6", "f6@filler6.test", "f6"},
				{"Bills", "Filed 7", "f7@filler7.test", "f7"},
				{"Bills", "Filed 8", "f8@filler8.test", "f8"},
				{"Bills", "Filed 9", "f9@filler9.test", "f9"},
				{"Bills", "Filed 10", "f10@filler10.test", "f10"},
				{"Receipts", "Kept 1", "k1@keeper1.test", "k1"},
				{"Receipts", "Kept 2", "k2@keeper2.test", "k2"},
				{"Receipts", "Kept 3", "k3@keeper3.test", "k3"},
				{"Receipts", "Kept 4", "k4@keeper4.test", "k4"},
				{"Receipts", "Kept 5", "k5@keeper5.test", "k5"},
				{"Receipts", "Kept 6", "k6@keeper6.test", "k6"},
				{"Receipts", "Kept 7", "k7@keeper7.test", "k7"},
				{"Receipts", "Kept 8", "k8@keeper8.test", "k8"},
				{"Receipts", "Kept 9", "k9@keeper9.test", "k9"},
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
	d := mustConnect(t, m.path, db.Key{})
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
		if n := m.count(t, "SELECT COUNT(*) FROM effects WHERE origin = 'mail_auto'"); n != 0 {
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
		if n := m.count(t, "SELECT COUNT(*) FROM effects WHERE origin = 'mail_auto'"); n != 0 {
			t.Fatalf("a paused pass recorded %d automatic actions", n)
		}

		m.run(t, "live", "resume")
		m.run(t, "sync")
		if got := m.verbs(); len(got) != 3 || got[0] != "COPY" || got[1] != "STORE" || got[2] != "EXPUNGE" {
			t.Fatalf("after resume the write log = %v, want COPY, STORE, EXPUNGE", got)
		}
		if state := m.scalar(t, "SELECT state FROM effects WHERE origin = 'mail_auto' AND operation = 'correct'"); state != "done" {
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

		if state := m.scalar(t, "SELECT state FROM effects WHERE origin = 'mail_auto' AND operation = 'correct'"); state != "skipped" {
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
		seedSettings(t, map[string]string{mail.KeyWriteCap: "1"})
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
		if n := m.count(t, "SELECT COUNT(*) FROM effects WHERE origin = 'mail_auto' AND state = 'done'"); n != 1 {
			t.Fatalf("with a cap of one %d actions applied, want 1", n)
		}
		if n := m.count(t, "SELECT COUNT(*) FROM effects WHERE origin = 'mail_auto' AND message_id = ?", secondID); n != 0 {
			t.Fatalf("the deferred message has %d automatic actions", n)
		}

		seedSettings(t, map[string]string{mail.KeyWriteCap: "2"})
		m.run(t, "sync")
		if n := m.copies(); n != 2 {
			t.Fatalf("after raising the cap the pass made %d copies, want 2", n)
		}
		if n := m.count(t, "SELECT COUNT(*) FROM effects WHERE origin = 'mail_auto' AND operation = 'correct' AND state = 'done'"); n != 2 {
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
		action := m.scalar(t, "SELECT CAST(id AS TEXT) FROM effects WHERE origin = 'mail_auto' AND operation = 'correct'")
		if state := m.scalar(t, "SELECT state FROM effects WHERE id = ?", action); state != "done" {
			t.Fatalf("forward correction state = %s, want done", state)
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
		if undone := m.scalar(t, "SELECT CAST(undone_by IS NOT NULL AS TEXT) FROM effects WHERE id = ?", action); undone != "1" {
			t.Fatalf("the action after undo is %s, want undone", undone)
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
		if state := m.scalar(t, "SELECT state FROM effects WHERE undo_of = ? ORDER BY id DESC LIMIT 1", action); state != "unknown" {
			t.Fatalf("the undo after a dropped connection is %s, want unknown", state)
		}
		if ran := runApp(t, "live", "undo", action); ran.code == cli.ExitOK {
			t.Fatalf("a second undo of a blocked undo exited %d", ran.code)
		}
		if n := len(m.fake.Boxes["Bills"].Box.Messages); n != 1 {
			t.Fatalf("the destination holds %d messages, want the blocked undo left alone", n)
		}
		m.run(t, "live", "resolve", action, "--outcome", "undone")
		if undone := m.scalar(t, "SELECT CAST(undone_by IS NOT NULL AS TEXT) FROM effects WHERE id = ?", action); undone != "1" {
			t.Fatalf("after the operator resolution the action is %s, want undone", undone)
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

	action := m.scalar(t, "SELECT CAST(id AS TEXT) FROM effects WHERE origin = 'mail_auto' AND operation = 'correct'")
	if state := m.scalar(t, "SELECT state FROM effects WHERE id = ?", action); state != "unknown" {
		t.Fatalf("a move whose copy was dropped is recorded as %s, want unknown", state)
	}
	if recorded := m.scalar(t, "SELECT CAST(error IS NOT NULL AS TEXT) FROM effects WHERE id = ?", action); recorded != "1" {
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
	if state := m.scalar(t, "SELECT state FROM effects WHERE id = ?", action); state != "unknown" {
		t.Fatalf("the next pass changed the blocked move to %s", state)
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
	if state := m.scalar(t, "SELECT state FROM effects WHERE id = ?", action); state != "unknown" {
		t.Fatalf("the correction pass changed the blocked move to %s", state)
	}

	m.run(t, "live", "resolve", action, "--outcome", "not-applied")
	if state := m.scalar(t, "SELECT state FROM effects WHERE id = ?", action); state != "unknown" {
		t.Fatalf("the resolution rewrote the original outcome to %s", state)
	}
	if recorded := m.scalar(t, "SELECT effective_outcome FROM effect_resolutions WHERE effect_id = ? ORDER BY id DESC LIMIT 1", action); recorded != "failed" {
		t.Fatalf("the resolution recorded %s, want failed", recorded)
	}
	before := len(m.fake.Writes)
	m.run(t, "sync")
	if got := len(m.fake.Writes) - before; got != 3 {
		t.Fatalf("the pass after the resolution made %d writes, want COPY, STORE, EXPUNGE", got)
	}
	if state := m.scalar(t, "SELECT state FROM effects WHERE origin = 'mail_auto' AND operation = 'correct' ORDER BY id DESC LIMIT 1"); state != "done" {
		t.Fatalf("the pass after the resolution left the action %s, want done", state)
	}
	if n := len(m.fake.Boxes["Bills"].Box.Messages); n != 1 {
		t.Fatalf("the destination holds %d messages after the resolution, want 1", n)
	}
}

func TestMailUndoneActionsDoNotRunAgain(t *testing.T) {
	const from = "statements@bank.test"

	t.Run("flag", func(t *testing.T) {
		const from = "alice@example.com"
		m := mailSafetyFixture(t, "live")
		contactsDir := filepath.Join(m.dir, "contacts")
		if err := os.MkdirAll(filepath.Join(contactsDir, "card"), 0o777); err != nil {
			t.Fatal(err)
		}
		card := "BEGIN:VCARD\nVERSION:3.0\nFN:Bank\nEMAIL:" + from + "\nEND:VCARD\n"
		if err := os.WriteFile(filepath.Join(contactsDir, "card", "bank.vcf"), []byte(card), 0o666); err != nil {
			t.Fatal(err)
		}
		setEnv(t, settingEnv(contacts.DirKey), contactsDir)
		uid := m.add("INBOX", "Question about your account", from, "target", "Can you confirm the transfer?")
		m.run(t, "sync")
		action := m.scalar(t, "SELECT CAST(id AS TEXT) FROM effects WHERE origin = 'mail_auto' AND operation = 'flag'")
		if state := m.scalar(t, "SELECT state FROM effects WHERE id = ?", action); state != "done" {
			t.Fatalf("the flag action is %s, want done", state)
		}
		m.run(t, "live", "undo", action)
		if undone := m.scalar(t, "SELECT CAST(undone_by IS NOT NULL AS TEXT) FROM effects WHERE id = ?", action); undone != "1" {
			t.Fatalf("the flag after undo is %s, want undone", undone)
		}
		if slices.Contains(m.fake.Boxes["INBOX"].Box.Messages[uid].Flags, `\Flagged`) {
			t.Fatalf("the undo left the message flagged: %v", m.fake.Boxes["INBOX"].Box.Messages[uid].Flags)
		}
		before := len(m.fake.Writes)
		m.run(t, "sync")
		if got := len(m.fake.Writes) - before; got != 0 {
			t.Fatalf("the pass after the undo made %d writes, want the undone flag left alone", got)
		}
		if slices.Contains(m.fake.Boxes["INBOX"].Box.Messages[uid].Flags, `\Flagged`) {
			t.Fatalf("the pass after the undo flagged the message again: %v", m.fake.Boxes["INBOX"].Box.Messages[uid].Flags)
		}
		if n := m.count(t, "SELECT COUNT(*) FROM effects WHERE origin = 'mail_auto' AND operation = 'flag'"); n != 1 {
			t.Fatalf("%d flag actions, want the undone one not repeated", n)
		}
	})

	t.Run("trash", func(t *testing.T) {
		m := mailSafetyFixture(t, "live")
		m.add("Bills", "Old statement", from, "old", "Your statement is ready.", `\Seen`)
		m.run(t, "sync")
		path := filepath.Join(m.dir, "rules.json")
		body := `{"rules":[{"id":"old","account":"icloud","action":"delete",` +
			`"when":[{"field":"from_addr","op":"eq","value":"` + from + `"}]}]}`
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		m.run(t, "rules", "import", path)
		m.run(t, "live", "deletions")
		action := m.scalar(t, "SELECT CAST(id AS TEXT) FROM effects WHERE origin = 'mail_auto' AND operation = 'trash'")
		if state := m.scalar(t, "SELECT state FROM effects WHERE id = ?", action); state != "done" {
			t.Fatalf("the trash action is %s, want done", state)
		}
		if n := len(m.fake.Boxes["Bills"].Box.Messages); n != 0 {
			t.Fatalf("Bills holds %d messages after the trash, want 0", n)
		}
		m.run(t, "live", "undo", action)
		if n := len(m.fake.Boxes["Bills"].Box.Messages); n != 1 {
			t.Fatalf("Bills holds %d messages after the undo, want 1", n)
		}
		m.run(t, "sync")
		before := len(m.fake.Writes)
		m.run(t, "live", "deletions")
		if got := len(m.fake.Writes) - before; got != 0 {
			t.Fatalf("the deletion pass after the undo made %d writes, want the restored message left alone", got)
		}
		if n := len(m.fake.Boxes["Bills"].Box.Messages); n != 1 {
			t.Fatalf("Bills holds %d messages after the second deletion pass, want the message where the operator put it", n)
		}
		if n := m.count(t, "SELECT COUNT(*) FROM effects WHERE origin = 'mail_auto' AND operation = 'trash'"); n != 1 {
			t.Fatalf("%d trash actions, want the undone one not repeated", n)
		}
	})
}

func TestMailInterruptedExpungeAfterCopyIsUnknown(t *testing.T) {
	m := mailSafetyFixture(t, "live")
	uid := m.add("INBOX", "Bank statement", "statements@bank.test", "target", "Your statement is ready.")
	m.run(t, "sync")
	id := m.messageID(t, "Bank statement")
	m.run(t, "live", "rebaseline")
	m.exec(t, "INSERT INTO corrections (message_id, account, folder, ts, source) VALUES (?, 'icloud', 'Bills', ?, 'operator')",
		id, mail.WallClock()+60)
	m.fake.Boxes["INBOX"].Box.SetFlags(uid, []string{`\Seen`}, nil)
	m.fake.FailWrite = func(w mailtest.FakeWrite) error {
		if w.Verb == "EXPUNGE" {
			return &mail.WriteInterrupted{Command: "UID EXPUNGE", Err: errors.New("connection reset by peer")}
		}
		return nil
	}
	if ran := runApp(t, "sync"); ran.code == cli.ExitOK {
		t.Fatalf("a pass whose expunge was interrupted exited 0")
	}
	action := m.scalar(t, "SELECT CAST(id AS TEXT) FROM effects WHERE origin = 'mail_auto' AND operation = 'correct'")
	if state := m.scalar(t, "SELECT state FROM effects WHERE id = ?", action); state != "unknown" {
		t.Fatalf("an interrupted expunge after a confirmed copy is recorded as %s, want unknown", state)
	}
	if copied := m.scalar(t, "SELECT after_json FROM effects WHERE id = ?", action); !strings.Contains(copied, `"uid"`) {
		t.Fatalf("the unknown move lost its confirmed copy evidence: %s", copied)
	}
	if n := len(m.fake.Boxes["Bills"].Box.Messages); n != 1 {
		t.Fatalf("Bills holds %d messages after the interrupted expunge, want the confirmed copy", n)
	}
	if ran := runApp(t, "live", "undo", action); ran.code == cli.ExitOK {
		t.Fatalf("an unknown move was offered a direct undo")
	}
	if state := m.scalar(t, "SELECT state FROM effects WHERE id = ?", action); state != "unknown" {
		t.Fatalf("the refused undo changed the action to %s", state)
	}
}

func TestMailPassRenewsItsLeaseBetweenGroups(t *testing.T) {
	const from = "statements@bank.test"
	m := mailSafetyFixture(t, "live")
	m.add("INBOX", "Statement one", from, "one", "Your statement is ready.")
	m.add("Bills", "Statement two", from, "two", "Your statement is ready.")
	m.run(t, "sync")
	m.run(t, "live", "rebaseline")
	first := m.messageID(t, "Statement one")
	second := m.messageID(t, "Statement two")
	for _, row := range []struct {
		id   int64
		dest string
	}{{first, "Bills"}, {second, "Receipts"}} {
		m.exec(t, "INSERT INTO corrections (message_id, account, folder, ts, source) VALUES (?, 'icloud', ?, ?, 'operator')",
			row.id, row.dest, mail.WallClock()+60)
	}
	for _, box := range []string{"INBOX", "Bills"} {
		for uid := range m.fake.Boxes[box].Box.Messages {
			m.fake.Boxes[box].Box.SetFlags(uid, []string{`\Seen`}, nil)
		}
	}
	backdated := 0
	m.fake.FailWrite = func(w mailtest.FakeWrite) error {
		if w.Verb == "COPY" && backdated == 0 {
			m.exec(t, "UPDATE effects SET lease_at = 0 WHERE state = 'intent'")
			backdated = m.count(t, "SELECT COUNT(*) FROM effects WHERE state = 'intent' AND lease_at = 0")
		}
		return nil
	}
	m.run(t, "sync")
	if backdated < 2 {
		t.Fatalf("backdated %d intents, want the whole pass in flight", backdated)
	}
	later := max(first, second)
	if state := m.scalar(t, "SELECT state FROM effects WHERE message_id = ? AND operation = 'correct'", later); state != "done" {
		t.Fatalf("the later group's action is %s, want done", state)
	}
	if lease := m.scalar(t, "SELECT CAST(lease_at AS TEXT) FROM effects WHERE message_id = ? AND operation = 'correct'", later); lease == "0" || lease == "0.0" {
		t.Fatalf("the later group's intent kept its expired lease (%s), want it renewed before the group ran", lease)
	}
	if n := len(m.fake.Boxes["Bills"].Box.Messages); n != 1 {
		t.Fatalf("Bills holds %d messages, want the first move", n)
	}
	if n := len(m.fake.Boxes["Receipts"].Box.Messages); n != 1 {
		t.Fatalf("Receipts holds %d messages, want the second move", n)
	}
}

func TestMailGroupSettledMeanwhileSettlesTheRest(t *testing.T) {
	const from = "statements@bank.test"
	m := mailSafetyFixture(t, "live")
	m.add("INBOX", "Statement one", from, "one", "Your statement is ready.")
	m.add("INBOX", "Statement two", from, "two", "Your statement is ready.")
	m.run(t, "sync")
	m.run(t, "live", "rebaseline")
	for _, subject := range []string{"Statement one", "Statement two"} {
		m.exec(t, "INSERT INTO corrections (message_id, account, folder, ts, source) VALUES (?, 'icloud', 'Bills', ?, 'operator')",
			m.messageID(t, subject), mail.WallClock()+60)
	}
	for uid := range m.fake.Boxes["INBOX"].Box.Messages {
		m.fake.Boxes["INBOX"].Box.SetFlags(uid, []string{`\Seen`}, nil)
	}
	recovered := false
	m.fake.FailWrite = func(w mailtest.FakeWrite) error {
		if w.Verb == "COPY" && !recovered {
			recovered = true
			m.exec(t, "UPDATE effects SET state = 'unknown', settled_at = lease_at WHERE id = "+
				"(SELECT MIN(id) FROM effects WHERE state = 'intent')")
		}
		return nil
	}
	m.run(t, "sync")
	if n := m.count(t, "SELECT COUNT(*) FROM effects WHERE state = 'intent'"); n != 0 {
		t.Fatalf("%d intents left open after the group failed, want every entry settled", n)
	}
	if n := m.count(t, "SELECT COUNT(*) FROM effects WHERE state = 'unknown'"); n != 2 {
		t.Fatalf("%d unknown actions, want the recovered one and the one that never ran", n)
	}
}

package main

import (
	"path/filepath"
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
	setEnv(t, settingEnv(config.KeyMailAccountsFile), accountsFile(t, dir, mailAccount{"work", preset, "atrium.test@example.test", "app-pass", false}))
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

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/executors"
	"github.com/jeiang/atrium/internal/mail/mailops"
	"github.com/jeiang/atrium/internal/mail/mailtest"
	"github.com/jeiang/atrium/internal/module"
)

func TestMailTriageSnoozeRecordsReturnWhenMailIsDisabledAfterMove(t *testing.T) {
	fake := mailtest.NewFakeClient()
	inbox := fake.AddBox("INBOX", mailtest.NewBox(1))
	fake.AddBox("Snoozed", mailtest.NewBox(7))
	h, x := mailtest.RawWith("Snoozed receipt", "shop@store.test", "snoozed-3", "Your receipt.")
	inbox.Add(h, x, []string{`\Seen`}, nil)
	m := mailEffectsFixture(t, "live", "icloud", fake)
	m.run(t, "sync")
	id := m.messageID(t, "Snoozed receipt")

	d := mustConnect(t, m.path, db.Key{})
	snaps, err := mailops.Snapshots(context.Background(), d.Read, []int64{id})
	if err != nil || len(snaps) != 1 {
		t.Fatalf("snapshot of the message: %v (%d)", err, len(snaps))
	}
	payload, err := json.Marshal(map[string]any{"items": []executors.TriageItem{{
		Snapshot: snaps[0], Action: executors.TriageSnooze, DueTS: mail.WallClock() + 3600,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	m.exec(t, "INSERT INTO proposals (id, kind, account, status, created_ts, title, payload, preview, fingerprint) "+
		"VALUES (900, 'mail_triage', 'work', 'pending', ?, 'snooze receipt', ?, '{}', 'fp-snooze')", mail.WallClock(), string(payload))

	startServeIn(t, m.dir)
	waitUntil(t, 30*time.Second, "the serve watch to finish its first pass and idle", func() bool { return fake.Idling() == 1 })
	arrived := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	var arrivedOnce sync.Once
	fake.StatusHook = func(name string, count uint32) uint32 {
		if name != "Snoozed" {
			return count
		}
		arrivedOnce.Do(func() {
			close(arrived)
			<-release
		})
		return count
	}
	result := make(chan appRun, 1)
	go func() { result <- asstRunApp(m.app, context.Background(), "proposals", "approve", fmt.Sprint(900)) }()
	select {
	case <-arrived:
	case <-time.After(10 * time.Second):
		t.Fatal("the snooze did not reach the move")
	}
	asstSet(t, module.FlagKey(module.Mail), "false")
	once.Do(func() { close(release) })
	ran := <-result
	fake.StatusHook = nil
	if ran.code != cli.ExitOK {
		t.Fatalf("approve after a disable mid-move: %d %q %q", ran.code, ran.stdout, ran.stderr)
	}
	if status := m.scalar(t, "SELECT status FROM proposals WHERE id = 900"); status != "applied" {
		t.Fatalf("the snooze proposal is %s, want applied", status)
	}
	if state := m.scalar(t, "SELECT state FROM snoozes WHERE message_id = ? AND proposal_id = 900", id); state != "pending" {
		t.Fatalf("the snooze row is %s after the move, want pending", state)
	}
	if n := len(fake.Boxes["Snoozed"].Box.Messages); n != 1 {
		t.Fatalf("the snoozed box holds %d messages, want 1", n)
	}
}

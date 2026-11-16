package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/apps/mail"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/mail/executors"
	"github.com/jeiang/atrium/internal/mail/mailops"
	"github.com/jeiang/atrium/internal/mail/mailtest"
)

func TestMailRetiredSnoozeRowsStayReadableAndNeverApply(t *testing.T) {
	fake := mailtest.NewFakeClient()
	inbox := fake.AddBox("INBOX", mailtest.NewBox(1))
	h, x := mailtest.RawWith("Old receipt", "shop@store.test", "retired-1", "Your receipt.")
	inbox.Add(h, x, []string{`\Seen`}, nil)
	m := mailEffectsFixture(t, "live", "icloud", fake)
	m.run(t, "sync")
	id := m.messageID(t, "Old receipt")

	d := mustConnect(t, m.path, db.Key{})
	snaps, err := mailops.Snapshots(context.Background(), d.Read, []int64{id})
	if err != nil || len(snaps) != 1 {
		t.Fatalf("snapshot of the message: %v (%d)", err, len(snaps))
	}
	item := executors.TriageItem{Snapshot: snaps[0], Action: "snooze"}.Dict()
	item["due_ts"] = mail.WallClock() + 3600
	payload, err := json.Marshal(map[string]any{"items": []any{item}})
	if err != nil {
		t.Fatal(err)
	}
	preview, err := json.Marshal(map[string]any{"count": 1, "messages": []any{map[string]any{"id": id, "subject": "Old receipt", "from_addr": "shop@store.test", "action": "snooze"}}})
	if err != nil {
		t.Fatal(err)
	}
	now := mail.WallClock()
	m.exec(t, "INSERT INTO proposals (id, kind, account, status, created_ts, title, payload, preview, fingerprint) "+
		"VALUES (900, 'mail_triage', 'work', 'pending', ?, 'Triage 1 messages', ?, ?, 'fp-retired-snooze')", now, string(payload), string(preview))
	m.exec(t, "INSERT INTO effects (id, created_at, lease_at, token, origin, operation, operation_key, direction, account, message_id, src, dest, target, action_json, after_json, state, settled_at) "+
		"VALUES (9201, ?, ?, 'retired-return', 'mail_auto', 'snooze_return', 'retired-return', 'forward', 'work', ?, 'Snoozed', 'INBOX', ?, '{}', '{}', 'done', ?)", now-120, now-120, id, fmt.Sprintf("message:%d", id), now-120)
	m.exec(t, "INSERT INTO effects (id, created_at, lease_at, token, origin, operation, operation_key, direction, account, target, action_json, after_json, undo_json, state, settled_at) "+
		"VALUES (9202, ?, ?, 'retired-snooze', 'proposal', 'snooze', 'retired-snooze', 'forward', 'work', 'snooze:1', '{\"op\":\"snooze\"}', '{\"snooze_id\":1}', '{\"op\":\"cancel_snooze\",\"snooze_id\":1}', 'done', ?)", now-60, now-60, now-60)

	m.run(t, "live", "actions")
	writes := len(fake.Writes)
	if ran := runApp(t, "proposals", "approve", "900"); ran.code == cli.ExitOK {
		t.Fatalf("approving a retired snooze item exited 0: %q", ran.stdout)
	}
	if status := m.scalar(t, "SELECT status FROM proposals WHERE id = 900"); status == "applied" {
		t.Fatalf("the retired snooze proposal is %s", status)
	}
	if got := fake.Writes[writes:]; len(got) != 0 {
		t.Fatalf("approving a retired snooze item wrote to IMAP: %v", got)
	}
	if n := len(fake.Boxes["INBOX"].Box.Messages); n != 1 {
		t.Fatalf("the inbox holds %d messages, want 1", n)
	}

	base := startServeIn(t, m.dir)
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	for _, path := range []string{"/review", "/proposals/900/card", "/activity"} {
		if code, page, _ := fetch(t, client, http.MethodGet, base+path, nil); code != http.StatusOK {
			t.Fatalf("%s = %d %.400s", path, code, page)
		}
	}
	code, body, _ := fetch(t, client, http.MethodPost, base+"/activity/effect/9202/undo", url.Values{})
	if code >= http.StatusInternalServerError || !strings.Contains(body, "unknown undo kind") {
		t.Fatalf("undo of a retired snooze = %d %.400s", code, body)
	}
	if state := m.scalar(t, "SELECT state FROM effects WHERE id = 9202"); state != "done" {
		t.Fatalf("the retired snooze effect is %s after a refused undo, want done", state)
	}
}

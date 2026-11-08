package main

import (
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/cli"
)

func TestAttentionToolOrdersGroupsSnoozesAndDropsDisabledModules(t *testing.T) {
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	if ran := runApp(t, "agent", "run", "--text", "warm"); ran.code != cli.ExitOK {
		t.Fatalf("warm run: %d %s", ran.code, ran.stderr)
	}
	d := mustConnect(t, dir+"/atrium.db")
	now := float64(time.Now().Unix())
	today := time.Unix(int64(now), 0).UTC()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := d.Write.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec("INSERT INTO proposals (id, kind, status, created_ts, title, fingerprint) VALUES (1, 'reminder', 'pending', 100, 'Call the dentist', 'fp1')")
	exec("INSERT INTO drafts (id, account, to_addrs, subject, body, status, created_at, updated_at) VALUES (1, 'a', 'pat@person.test', 'Re: lease', '', 'draft', 200, 200)")
	exec("INSERT INTO open_loops (thread_key, account, state, last_message_id, since, counterpart, subject, updated_ts) VALUES ('k1', 'a', 'owe_reply', 1, 150, 'pat', 'Dinner?', 150)")
	exec("INSERT INTO open_loops (thread_key, account, state, last_message_id, since, counterpart, subject, updated_ts) VALUES ('k2', 'a', 'waiting_on_them', 2, 120, 'sam', 'Contract', 120)")
	exec("INSERT INTO commitments (id, message_id, text, owner, due, status, created_ts) VALUES (1, 1, 'Send invoice', 'me', ?, 'open', 300)", today.AddDate(0, 0, 2).Format("2006-01-02"))
	exec("INSERT INTO commitments (id, message_id, text, owner, due, status, created_ts) VALUES (2, 1, 'Book the room', 'me', ?, 'open', 301)", today.AddDate(0, 0, -3).Format("2006-01-02"))
	exec("INSERT INTO event_candidates (message_id, reason, status, ts) VALUES (2, 'invite', 'pending', 250)")
	exec("INSERT INTO thread_plans (thread_id, items_json, updated_at) VALUES (1, '[]', 400)")
	exec("INSERT INTO packages (id, merchant, status, status_text, tracking_number, source_message_id, last_message_id, updated_ts, first_seen) VALUES (1, 'Bookshop', 'transit', 'In transit', 'TRACK', 1, 1, 500, 500)")

	run := func() string {
		t.Helper()
		api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("a1", "attention", map[string]any{})))
		api.Push(anthropictest.Reply("end_turn", anthropictest.Text("done")))
		ran := runApp(t, "agent", "run", "--kind", "cron", "--text", "attention")
		if ran.code != cli.ExitOK {
			t.Fatalf("attention run: %d %s", ran.code, ran.stderr)
		}
		var threadID int64
		if err := d.Read.QueryRow("SELECT MAX(id) FROM threads").Scan(&threadID); err != nil {
			t.Fatal(err)
		}
		rows, err := d.Read.Query("SELECT content_json FROM thread_messages WHERE thread_id = ? AND role = 'user' ORDER BY seq", threadID)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = rows.Close() }()
		var all strings.Builder
		for rows.Next() {
			var content string
			if err := rows.Scan(&content); err != nil {
				t.Fatal(err)
			}
			all.WriteString(content)
		}
		return all.String()
	}
	body := run()
	for _, want := range []string{"Call the dentist", "Send invoice", "Dinner?", "In transit", "Overdue", "Due soon", "Needs decision", "Waiting on them", "Upcoming"} {
		if !strings.Contains(body, want) {
			t.Fatalf("attention result misses %q: %s", want, body)
		}
	}
	exec("INSERT INTO attention_snoozes (kind, source_id, until, created_ts) VALUES ('proposal', '1', ?, ?)", now+3600, now)
	body = run()
	if strings.Contains(body, "Call the dentist") {
		t.Fatalf("snoozed proposal is still listed")
	}
	exec("UPDATE attention_snoozes SET until = ?", now-1)
	body = run()
	if !strings.Contains(body, "Call the dentist") {
		t.Fatalf("snooze did not expire")
	}
	setEnv(t, flagEnv("mail"), "false")
	body = run()
	for _, gone := range []string{"Re: lease", "Dinner?", "Send invoice", "In transit"} {
		if strings.Contains(body, gone) {
			t.Fatalf("mail item %q survived a disabled mail module: %s", gone, body)
		}
	}
	if !strings.Contains(body, "Call the dentist") {
		t.Fatalf("core proposal disappeared with mail disabled")
	}
}

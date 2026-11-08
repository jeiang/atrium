package main

import (
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/cli"
)

func TestTaskAttentionSource(t *testing.T) {
	dir, api, _ := moduleFixture(t, flagsExcept("mail", "false"))
	if ran := runApp(t, "agent", "run", "--text", "warm"); ran.code != cli.ExitOK {
		t.Fatalf("warm run: %d %s", ran.code, ran.stderr)
	}
	d := mustConnect(t, dir+"/atrium.db")
	now := float64(time.Now().Unix())
	due := time.Now().AddDate(0, 0, -1).Format("2006-01-02")
	res, err := d.Write.Exec("INSERT INTO tasks (title, due, status, priority, created_ts, updated_ts) VALUES ('Pay the invoice', ?, 'open', 'high', ?, ?)", due, now, now)
	if err != nil {
		t.Fatal(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	runAttention := func() string {
		t.Helper()
		api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("a1", "attention", map[string]any{})))
		api.Push(anthropictest.Reply("end_turn", anthropictest.Text("done")))
		if ran := runApp(t, "agent", "run", "--kind", "cron", "--text", "attention"); ran.code != cli.ExitOK {
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
	body := runAttention()
	for _, want := range []string{"Pay the invoice", "overdue", "Done", "Dismiss", "Snooze"} {
		if !strings.Contains(body, want) {
			t.Fatalf("attention result misses %q: %s", want, body)
		}
	}
	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("d1", "task_done", map[string]any{"id": id})))
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("done")))
	if ran := runApp(t, "agent", "run", "--text", "complete"); ran.code != cli.ExitOK {
		t.Fatalf("task_done run: %d %s", ran.code, ran.stderr)
	}
	body = runAttention()
	if strings.Contains(body, "Pay the invoice") {
		t.Fatalf("completed task is still listed: %s", body)
	}
}

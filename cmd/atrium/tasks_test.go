package main

import (
	"context"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/proposals"
)

func TestTaskAddUndoAndRegistryMatrix(t *testing.T) {
	dir, api := moduleFixture(t, flagsExcept("", ""))
	if ran := runApp("agent", "run", "--text", "warm"); ran.code != cli.ExitOK {
		t.Fatalf("warm run: %d %s", ran.code, ran.stderr)
	}
	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("t1", "task_add", map[string]any{"title": "Call the dentist", "due": "2026-11-09"})))
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("added")))
	if ran := runApp("agent", "run", "--thread", "1", "--text", "add a task"); ran.code != cli.ExitOK {
		t.Fatalf("task_add run: %d %s", ran.code, ran.stderr)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
	var taskID int64
	if err := d.Read.QueryRow("SELECT id FROM tasks WHERE title = 'Call the dentist'").Scan(&taskID); err != nil {
		t.Fatal(err)
	}
	base, _, stop := startServeStoppable(t, dir)
	defer stop()
	var writeID int64
	if err := d.Read.QueryRow("SELECT id FROM agent_writes WHERE tool = 'task_add' ORDER BY id DESC LIMIT 1").Scan(&writeID); err != nil {
		t.Fatal(err)
	}
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	if code, _, _ := fetch(t, client, http.MethodGet, base+"/activity", nil); code != http.StatusOK {
		t.Fatalf("activity page = %d", code)
	}
	if code, body, _ := fetch(t, client, http.MethodPost, base+"/activity/write/"+strconv.FormatInt(writeID, 10)+"/undo", url.Values{}); code != http.StatusOK {
		t.Fatalf("undo = %d %s", code, body)
	}
	var n int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM tasks WHERE id = ?", taskID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("task_add undo did not delete the task")
	}
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("hi")))
	cron := runApp("agent", "run", "--kind", "cron", "--text", "hi")
	cronNames := offeredTools(t, api, cron)
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("hi")))
	nightly := runApp("agent", "run", "--kind", "nightly", "--text", "hi")
	nightlyNames := offeredTools(t, api, nightly)
	for _, name := range []string{"task_list", "task_propose"} {
		if !containsName(cronNames, name) || !containsName(nightlyNames, name) {
			t.Fatalf("%s missing from cron/nightly: %v %v", name, cronNames, nightlyNames)
		}
	}
	for _, name := range []string{"task_add", "task_update", "task_done"} {
		if containsName(cronNames, name) || containsName(nightlyNames, name) {
			t.Fatalf("%s offered to an unattended run", name)
		}
	}
}

func containsName(names []string, want string) bool {
	for _, name := range names {
		if name == want {
			return true
		}
	}
	return false
}

func TestReminderProposalCreatesATaskAndUndoRemovesIt(t *testing.T) {
	dir, _ := moduleFixture(t, flagsExcept("", ""))
	if ran := runApp("agent", "run", "--text", "warm"); ran.code != cli.ExitOK {
		t.Fatalf("warm run: %d %s", ran.code, ran.stderr)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
	ctx := context.Background()
	now := float64(time.Now().Unix())
	payload := map[string]any{"text": "Pay the rent", "remind_at": now + 3600, "tz": "UTC", "message_id": 1}
	id, _, err := proposals.Create(ctx, d, "reminder", nil, "Pay the rent", payload, payload, "fp-reminder", now)
	if err != nil {
		t.Fatal(err)
	}
	if ran := runApp("proposals", "approve", strconv.FormatInt(id, 10)); ran.code != cli.ExitOK {
		t.Fatalf("approve: %d %s", ran.code, ran.stderr)
	}
	var taskID int64
	if err := d.Read.QueryRow("SELECT id FROM tasks WHERE source_kind = 'proposal' AND source_id = ?", strconv.FormatInt(id, 10)).Scan(&taskID); err != nil {
		t.Fatal(err)
	}
	var jobs int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM jobs WHERE name LIKE 'Reminder:%'").Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if jobs != 0 {
		t.Fatalf("reminder approval created %d scheduler jobs", jobs)
	}
	if ran := runApp("proposals", "undo", strconv.FormatInt(id, 10)); ran.code != cli.ExitOK {
		t.Fatalf("undo: %d %s", ran.code, ran.stderr)
	}
	var n int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM tasks WHERE id = ?", taskID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("reminder undo did not delete the task")
	}
}

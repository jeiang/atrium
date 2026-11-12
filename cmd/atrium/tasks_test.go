package main

import (
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/proposals"
)

func TestTaskAddUndoAndRegistryMatrix(t *testing.T) {
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	if ran := runApp(t, "agent", "run", "--text", "warm"); ran.code != cli.ExitOK {
		t.Fatalf("warm run: %d %s", ran.code, ran.stderr)
	}
	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("t1", "task_add", map[string]any{"title": "Call the dentist", "due": "2026-11-09"})))
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("added")))
	if ran := runApp(t, "agent", "run", "--thread", "1", "--text", "add a task"); ran.code != cli.ExitOK {
		t.Fatalf("task_add run: %d %s", ran.code, ran.stderr)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), db.Key{})
	var taskID int64
	if err := d.Read.QueryRow("SELECT id FROM tasks WHERE title = 'Call the dentist'").Scan(&taskID); err != nil {
		t.Fatal(err)
	}
	base, _, stop := startServeStoppable(t, dir)
	defer stop()
	var effectID int64
	if err := d.Read.QueryRow("SELECT id FROM effects WHERE operation = 'task_add' AND direction = 'forward' ORDER BY id DESC LIMIT 1").Scan(&effectID); err != nil {
		t.Fatal(err)
	}
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	if code, body, _ := fetch(t, client, http.MethodGet, base+"/activity", nil); code != http.StatusOK || !strings.Contains(body, "/activity/effect/"+strconv.FormatInt(effectID, 10)+"/undo") {
		t.Fatalf("activity page = %d, want an undo for the task_add effect: %.400s", code, body)
	}
	if code, body, _ := fetch(t, client, http.MethodPost, base+"/activity/effect/"+strconv.FormatInt(effectID, 10)+"/undo", url.Values{}); code != http.StatusOK {
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
	cron := runApp(t, "agent", "run", "--kind", "cron", "--text", "hi")
	cronNames := offeredTools(t, api, cron)
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("hi")))
	nightly := runApp(t, "agent", "run", "--kind", "nightly", "--text", "hi")
	nightlyNames := offeredTools(t, api, nightly)
	for _, name := range []string{"task_list", "task_add"} {
		if !containsName(cronNames, name) || !containsName(nightlyNames, name) {
			t.Fatalf("%s missing from cron/nightly: %v %v", name, cronNames, nightlyNames)
		}
	}
	for _, name := range []string{"task_propose", "task_update", "task_done", "draft_email_create", "draft_email_update", "file_write",
		"mail_folder_create", "mail_move", "notion_add_alias", "notion_archive_page", "notion_create_page", "notion_move_page",
		"notion_update_page", "profile_set", "schedule_cancel", "schedule_create", "schedule_update"} {
		if containsName(cronNames, name) || containsName(nightlyNames, name) {
			t.Fatalf("%s offered to an unattended run", name)
		}
	}
	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("t2", "task_add", map[string]any{"title": "Renew the passport"})))
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("added")))
	if ran := runApp(t, "agent", "run", "--kind", "cron", "--text", "add a task"); ran.code != cli.ExitOK {
		t.Fatalf("cron task_add run: %d %s", ran.code, ran.stderr)
	}
	var cronTasks, proposed int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM tasks WHERE title = 'Renew the passport'").Scan(&cronTasks); err != nil {
		t.Fatal(err)
	}
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM proposals").Scan(&proposed); err != nil {
		t.Fatal(err)
	}
	if cronTasks != 1 || proposed != 0 {
		t.Fatalf("cron task_add made %d tasks and %d proposals, want one direct task", cronTasks, proposed)
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
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	if ran := runApp(t, "agent", "run", "--text", "warm"); ran.code != cli.ExitOK {
		t.Fatalf("warm run: %d %s", ran.code, ran.stderr)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), db.Key{})
	ctx := testProposalContext()
	now := float64(time.Now().Unix())
	payload := map[string]any{"text": "Pay the rent", "remind_at": now + 3600, "tz": "UTC", "message_id": 1}
	id, _, err := proposals.Create(ctx, d, "reminder", nil, "Pay the rent", payload, payload, "fp-reminder", now)
	if err != nil {
		t.Fatal(err)
	}
	if ran := runApp(t, "proposals", "approve", strconv.FormatInt(id, 10)); ran.code != cli.ExitOK {
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
	if ran := runApp(t, "proposals", "undo", strconv.FormatInt(id, 10)); ran.code != cli.ExitOK {
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

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"path/filepath"
	"testing"

	"github.com/jeiang/atrium/internal/anthropic"
	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
)

func agentToolCalls(t *testing.T, calls ...[2]any) {
	t.Helper()
	blocks := make([]json.RawMessage, len(calls))
	for i, c := range calls {
		blocks[i] = anthropictest.ToolUse(fmt.Sprintf("c%d", i), c[0].(string), c[1].(map[string]any))
	}
	api := anthropictest.New(t,
		anthropictest.Reply("tool_use", blocks...),
		anthropictest.Reply("end_turn", anthropictest.Text("done")),
	)
	t.Setenv(anthropic.BaseURLEnv, api.URL)
	ran := runApp("agent", "run", "--text", "please", "--kind", "chat")
	if ran.code != cli.ExitOK {
		t.Fatalf("agent run: %d %s", ran.code, ran.stderr)
	}
}

func scalar[T any](t *testing.T, d *db.DB, query string, args ...any) T {
	t.Helper()
	var v T
	if err := d.Read.QueryRow(query, args...).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestDraftAndScheduleWritesUndoThroughTheActivityPage(t *testing.T) {
	dir, _ := moduleFixture(t, flagsExcept("", ""))
	agentToolCalls(t,
		[2]any{"draft_email_create", map[string]any{"account": "icloud", "to": []string{"a@example.com"}, "subject": "Hi", "body": "first"}},
		[2]any{"schedule_create", map[string]any{"name": "keep", "prompt": "check", "schedule": "daily 09:00"}},
		[2]any{"schedule_create", map[string]any{"name": "drop", "prompt": "check", "schedule": "daily 10:00"}},
	)
	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
	keep := scalar[int64](t, d, "SELECT id FROM jobs WHERE name = 'keep'")
	drop := scalar[int64](t, d, "SELECT id FROM jobs WHERE name = 'drop'")
	agentToolCalls(t,
		[2]any{"draft_email_update", map[string]any{"draft_id": 1, "body": "second"}},
		[2]any{"schedule_update", map[string]any{"id": keep, "name": "renamed", "schedule": "daily 11:00", "enabled": false}},
		[2]any{"schedule_cancel", map[string]any{"id": drop}},
	)
	writeID := func(tool, target string) int64 {
		return scalar[int64](t, d, "SELECT id FROM agent_writes WHERE tool = ? AND target = ? ORDER BY id DESC", tool, target)
	}
	if n := scalar[int](t, d, "SELECT count(*) FROM agent_writes WHERE undo_json IS NOT NULL"); n != 6 {
		t.Fatalf("%d writes carry an undo, want 6", n)
	}

	base := startServeIn(t, dir)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if status, body, _ := fetch(t, client, "GET", base+"/activity?category=assistant", nil); status != http.StatusOK {
		t.Fatalf("GET /activity = %d %s", status, body)
	}
	undo := func(id int64, want int) {
		t.Helper()
		if status, body, _ := fetch(t, client, "POST", fmt.Sprintf("%s/activity/write/%d/undo", base, id), url.Values{}); status != want {
			t.Fatalf("undo %d = %d, want %d: %s", id, status, want, body)
		}
	}
	draft := func() (string, string) {
		return scalar[string](t, d, "SELECT status FROM drafts WHERE id = 1"), scalar[string](t, d, "SELECT body FROM drafts WHERE id = 1")
	}

	created, updated := writeID("draft_email_create", "draft:1"), writeID("draft_email_update", "draft:1")
	undo(created, http.StatusConflict)
	undo(updated, http.StatusSeeOther)
	if status, body := draft(); status != "draft" || body != "first" {
		t.Errorf("draft after undoing the edit = %s %q", status, body)
	}
	undo(updated, http.StatusConflict)
	undo(created, http.StatusSeeOther)
	if status, _ := draft(); status != "discarded" {
		t.Errorf("draft after undoing the create = %s", status)
	}

	target := func(id int64) string { return fmt.Sprintf("schedule:%d", id) }
	undo(writeID("schedule_update", target(keep)), http.StatusSeeOther)
	name, schedule, enabled := scalar[string](t, d, "SELECT name FROM jobs WHERE id = ?", keep),
		scalar[string](t, d, "SELECT schedule FROM jobs WHERE id = ?", keep), scalar[bool](t, d, "SELECT enabled FROM jobs WHERE id = ?", keep)
	if name != "keep" || schedule != "daily 09:00" || !enabled {
		t.Errorf("schedule after undoing the update = %q %q %v", name, schedule, enabled)
	}
	undo(writeID("schedule_cancel", target(drop)), http.StatusSeeOther)
	if cancelled, due := scalar[bool](t, d, "SELECT cancelled FROM jobs WHERE id = ?", drop), scalar[*float64](t, d, "SELECT next_due FROM jobs WHERE id = ?", drop); cancelled || due == nil {
		t.Errorf("schedule after undoing the cancel: cancelled=%v next_due=%v", cancelled, due)
	}
	undo(writeID("schedule_create", target(drop)), http.StatusSeeOther)
	if !scalar[bool](t, d, "SELECT cancelled FROM jobs WHERE id = ?", drop) {
		t.Error("undoing the create did not cancel the schedule")
	}
}

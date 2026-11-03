package main

import (
	"slices"
	"testing"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/notion"
)

const wikiRoot = "0123456789abcdef0123456789abcdef"

var undoableWrites = []string{
	"calendar_create", "calendar_delete", "draft_email_create", "draft_email_update", "media_add_magnet", "media_channel_add",
	"media_channel_remove", "notion_add_alias", "notion_archive_page", "notion_create_page", "notion_move_page", "notion_update_page",
	"profile_set", "schedule_cancel", "schedule_create", "schedule_update",
}

func toolsOffered(t *testing.T, api *modelLog, kind string) []string {
	t.Helper()
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("hi")))
	before := len(api.Requests())
	ran := runApp("agent", "run", "--text", "hello", "--kind", kind)
	if got := len(api.Requests()); got <= before {
		t.Fatalf("agent run --kind %s sent no model request: exit %d stderr %q", kind, ran.code, ran.stderr)
	}
	return offeredTools(t, api, ran)
}

func webSearchEntry(t *testing.T, api *modelLog) map[string]any {
	t.Helper()
	requests := api.Requests()
	tools, _ := requests[len(requests)-1].JSON(t)["tools"].([]any)
	for _, raw := range tools {
		if entry, ok := raw.(map[string]any); ok && entry["name"] == "web_search" {
			return entry
		}
	}
	return nil
}

func TestRunKindPolicyShapesTheOfferedTools(t *testing.T) {
	_, api := moduleFixture(t, flagsExcept("", ""))
	t.Setenv(settingEnv(notion.WikiRootKey), wikiRoot)

	chat := toolsOffered(t, api, "chat")
	for _, name := range undoableWrites {
		if !slices.Contains(chat, name) {
			t.Errorf("chat run does not offer %s", name)
		}
	}

	cron := toolsOffered(t, api, "cron")
	for _, name := range undoableWrites {
		if slices.Contains(cron, name) {
			t.Errorf("cron run offers write_undo tool %s", name)
		}
	}
	for _, name := range []string{"mail_search", "notify", "event_propose", "web_fetch", "web_search"} {
		if !slices.Contains(cron, name) {
			t.Errorf("cron run does not offer %s", name)
		}
	}

	nightly := toolsOffered(t, api, "nightly")
	for _, name := range []string{"notion_create_page", "notion_update_page", "threads_today"} {
		if !slices.Contains(nightly, name) {
			t.Errorf("nightly run does not offer %s", name)
		}
	}

	sub := toolsOffered(t, api, "sub")
	for _, name := range []string{"mail_read", "web_fetch"} {
		if !slices.Contains(sub, name) {
			t.Errorf("sub run does not offer read tool %s", name)
		}
	}
	for _, name := range append([]string{"notify", "event_propose", "calendar_update", "propose", "research"}, undoableWrites...) {
		if slices.Contains(sub, name) {
			t.Errorf("sub run offers non-read tool %s", name)
		}
	}
}

func TestToolEnabledSettingRemovesToolFromTheRequest(t *testing.T) {
	_, api := moduleFixture(t, flagsExcept("", ""))
	t.Setenv(settingEnv(notion.WikiRootKey), wikiRoot)
	for _, name := range []string{"mail_search", "notion_create_page", "web_fetch", "web_search"} {
		t.Setenv(settingEnv(assistant.ToolEnabledKey(name)), "false")
	}
	chat := toolsOffered(t, api, "chat")
	for _, name := range []string{"mail_search", "notion_create_page", "web_fetch", "web_search"} {
		if slices.Contains(chat, name) {
			t.Errorf("chat run offers disabled tool %s", name)
		}
	}
	for _, name := range []string{"mail_read", "notion_update_page", "notify"} {
		if !slices.Contains(chat, name) {
			t.Errorf("chat run lost enabled tool %s", name)
		}
	}
}

func TestWebSearchDynamicFilteringSettingTogglesAllowedCallers(t *testing.T) {
	_, api := moduleFixture(t, flagsExcept("", ""))
	t.Setenv(settingEnv(notion.WikiRootKey), wikiRoot)
	toolsOffered(t, api, "chat")
	entry := webSearchEntry(t, api)
	if entry == nil || !slices.Equal(callers(entry), []string{"direct"}) {
		t.Fatalf("web_search by default = %v", entry)
	}
	t.Setenv(settingEnv(assistant.KeyWebSearchDynamicFiltering), "true")
	toolsOffered(t, api, "chat")
	entry = webSearchEntry(t, api)
	if entry == nil {
		t.Fatal("web_search missing with dynamic filtering on")
	}
	if _, ok := entry["allowed_callers"]; ok {
		t.Errorf("web_search keeps allowed_callers with dynamic filtering on: %v", entry)
	}
}

func callers(entry map[string]any) []string {
	var out []string
	list, _ := entry["allowed_callers"].([]any)
	for _, c := range list {
		out = append(out, c.(string))
	}
	return out
}

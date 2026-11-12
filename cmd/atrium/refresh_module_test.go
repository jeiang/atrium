package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/module"
)

func TestWebRunnerRefreshesToolsAcrossModuleToggles(t *testing.T) {
	dir, api, f := moduleFixture(t, flagsExcept("", ""))
	asstWarm(t)
	message := seedMail(t, dir)
	d := asstDB(t, dir)
	base, _, _ := startServeApp(t, f.app, &lockedBuffer{})
	w := asstBrowse(t, base)

	arrived := make(chan struct{})
	release := make(chan struct{})
	var once, releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	previous := api.OnRequest
	api.OnRequest = func(r anthropictest.Request) {
		previous(r)
		once.Do(func() { close(arrived); <-release })
	}
	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("live-disabled-mail", "mail_read", map[string]any{"message_id": message})), anthropictest.Reply("end_turn", anthropictest.Text("refused")))
	thread := w.start(map[string]string{"text": "read my mail"})
	select {
	case <-arrived:
	case <-time.After(10 * time.Second):
		t.Fatal("model request did not arrive")
	}
	initial := api.Requests()
	var initiallyOffered bool
	tools, _ := initial[len(initial)-1].JSON(t)["tools"].([]any)
	for _, raw := range tools {
		if entry, ok := raw.(map[string]any); ok && entry["name"] == "mail_read" {
			initiallyOffered = true
		}
	}
	if !initiallyOffered {
		t.Fatal("mail_read was not offered before disable")
	}
	asstSet(t, module.FlagKey(module.Mail), "false")
	releaseOnce.Do(func() { close(release) })
	if state, detail := asstSettle(t, d, asstLastRun(t, d, thread)); state != "done" {
		t.Fatalf("disabled run: %s %s", state, detail)
	}
	api.OnRequest = previous
	result := findToolResult(t, api.Requests(), "live-disabled-mail")
	encoded, err := json.Marshal(result)
	if err != nil || result["is_error"] != true || !strings.Contains(string(encoded), "the mail module was disabled; start the action again") {
		t.Fatalf("disabled tool result = %s (%v)", encoded, err)
	}

	for _, enabled := range []bool{false, true} {
		asstSet(t, module.FlagKey(module.Mail), fmt.Sprint(enabled))
		start := len(api.Requests())
		api.Push(anthropictest.Reply("end_turn", anthropictest.Text("done")))
		code, body, _ := w.upload(fmt.Sprintf("/chat/%d/messages", thread), map[string]string{"text": "hello", "edit": ""}, nil, true)
		if code != http.StatusOK {
			t.Fatalf("follow-up: %d %s", code, body)
		}
		if state, detail := asstSettle(t, d, asstLastRun(t, d, thread)); state != "done" {
			t.Fatalf("follow-up: %s %s", state, detail)
		}
		requests := api.Requests()
		if len(requests) <= start {
			t.Fatal("follow-up made no model request")
		}
		var names []string
		tools, _ := requests[start].JSON(t)["tools"].([]any)
		for _, raw := range tools {
			if entry, ok := raw.(map[string]any); ok {
				if name, ok := entry["name"].(string); ok {
					names = append(names, name)
				}
			}
		}
		if slices.Contains(names, "mail_read") != enabled {
			t.Fatalf("mail_read offered = %v after enabled=%v: %v", !enabled, enabled, names)
		}
	}
	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("live-enabled-mail", "mail_read", map[string]any{"message_id": message})), anthropictest.Reply("end_turn", anthropictest.Text("read")))
	code, body, _ := w.upload(fmt.Sprintf("/chat/%d/messages", thread), map[string]string{"text": "read my mail", "edit": ""}, nil, true)
	if code != http.StatusOK {
		t.Fatalf("enabled follow-up: %d %s", code, body)
	}
	if state, detail := asstSettle(t, d, asstLastRun(t, d, thread)); state != "done" {
		t.Fatalf("enabled run: %s %s", state, detail)
	}
	if result := findToolResult(t, api.Requests(), "live-enabled-mail"); result == nil || result["is_error"] == true {
		t.Fatalf("enabled tool result = %v", result)
	}
}

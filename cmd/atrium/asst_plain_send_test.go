package main

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
)

func TestAsstComposerFollowUpWithPendingMemoryJobSucceeds(t *testing.T) {
	t.Parallel()
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	asstWarm(t)
	d := asstDB(t, dir)
	base, logs := startServeLogged(t, dir)
	w := asstBrowse(t, base)

	long := strings.Repeat("evidence ", 120)
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("ack")))
	thread := w.start(map[string]string{"text": long})
	if state, errText := asstSettle(t, d, asstLastRun(t, d, thread)); state != "done" {
		t.Fatalf("first run %s %q", state, errText)
	}
	asstWaitCount(t, d, 1, "SELECT CASE WHEN COUNT(*) > 0 THEN 1 ELSE 0 END FROM memory_jobs WHERE thread_id = ? AND state = 'queued'", thread)

	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("follow-up answer")))
	code, body, _ := w.upload(fmt.Sprintf("/chat/%d/messages", thread), map[string]string{"text": "plain follow-up", "edit": ""}, nil, true)
	if code != http.StatusOK {
		t.Fatalf("plain follow-up = %d: %s; server log: %s", code, body, logs.String())
	}
	if state, errText := asstSettle(t, d, asstLastRun(t, d, thread)); state != "done" {
		t.Fatalf("follow-up run %s %q", state, errText)
	}
	if n := asstCount(t, d, "SELECT COUNT(*) FROM thread_messages WHERE thread_id = ? AND superseded = 0", thread); n != 4 {
		t.Errorf("%d live messages after a plain follow-up, want 4", n)
	}
}

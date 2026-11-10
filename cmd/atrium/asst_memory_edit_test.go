package main

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
)

func TestAsstEditWithPendingMemoryJobSucceeds(t *testing.T) {
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

	before := asstLastRun(t, d, thread)
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("edited")))
	code, body, _ := w.upload(fmt.Sprintf("/chat/%d/messages", thread), map[string]string{"text": "short follow-up", "edit": "1"}, nil, true)
	if code != http.StatusOK {
		t.Fatalf("edit with a pending memory job = %d: %s; server log: %s", code, body, logs.String())
	}
	if asstLastRun(t, d, thread) <= before {
		t.Fatal("edit started no run")
	}
	if state, errText := asstSettle(t, d, asstLastRun(t, d, thread)); state != "done" {
		t.Fatalf("edited run %s %q", state, errText)
	}
	if n := asstCount(t, d, "SELECT COUNT(*) FROM memory_jobs WHERE thread_id = ? AND node_id NOT IN (SELECT id FROM memory_nodes WHERE thread_id = ?)", thread, thread); n != 0 {
		t.Errorf("%d memory jobs reference missing nodes", n)
	}
}

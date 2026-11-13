package main

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
)

func TestRunEventsPersistAcrossRuns(t *testing.T) {
	dir, api, f := moduleFixture(t, flagsExcept("", ""))
	warm(t, api)
	d := asstDB(t, dir)
	token := localAPIToken(t, "streams")
	base, _, stop, err := startServeAppArgs(t, f.app, &lockedBuffer{}, "--auth", "local")
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	s := &apiSite{t: t, base: base}
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("first stream")))
	made := s.do(http.MethodPost, "/api/v1/threads", token, map[string]any{"text": "first"})
	if made.status != http.StatusCreated {
		t.Fatalf("create: %d %s", made.status, made.body)
	}
	thread := made.json["thread"].(map[string]any)
	threadID, runID := number(t, thread["id"]), number(t, made.json["run_id"])
	first, _ := s.events(token, threadID, runID, "")
	if n := strings.Count(first, "event: complete\n"); n != 1 {
		t.Fatalf("%d terminal events: %s", n, first)
	}
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("second stream")))
	sent := s.do(http.MethodPost, fmt.Sprintf("/api/v1/threads/%d/messages", threadID), token, map[string]any{"text": "second"})
	if sent.status != http.StatusAccepted {
		t.Fatalf("send: %d %s", sent.status, sent.body)
	}
	s.events(token, threadID, number(t, sent.json["run_id"]), "")
	replay, _ := s.events(token, threadID, runID, "")
	if replay != first {
		t.Fatalf("a later run changed the first stream:\n%s\nwas:\n%s", replay, first)
	}
	if n := asstCount(t, d, "SELECT COUNT(*) FROM run_streams WHERE run_id=? AND terminal_seq=next_seq-1 AND terminal_at IS NOT NULL", runID); n != 1 {
		t.Fatalf("terminal metadata rows: %d", n)
	}
	if n := asstCount(t, d, "SELECT COUNT(*) FROM run_events WHERE stream_run_id=?", runID); n != len(asstFrameIDs(first)) {
		t.Fatalf("durable events: %d, frames %d", n, len(asstFrameIDs(first)))
	}
	stop()
	base, _, stop, err = startServeAppArgs(t, f.app, &lockedBuffer{}, "--auth", "local")
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	s.base = base
	replay, _ = s.events(token, threadID, runID, "")
	if replay != first {
		t.Fatal("restart changed the durable stream")
	}
	browser := asstBrowse(t, base)
	code, body, _ := browser.post(fmt.Sprintf("/chat/%d/delete", threadID), url.Values{}, true)
	if code != http.StatusOK && code != http.StatusSeeOther && code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", code, body)
	}

	if n := asstCount(t, d, "SELECT COUNT(*) FROM run_events WHERE run_id=? OR stream_run_id=?", runID, runID); n != 0 {
		t.Fatalf("%d events retained after deletion", n)
	}
}

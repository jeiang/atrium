package pages

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
)

func TestMemoryTreeTracksSupersedeAndBranch(t *testing.T) {
	s := newSite(t)
	s.api.Push(reply1(anthropictest.Text("first answer")))
	thread := s.startThread("hello")
	s.settle(s.lastRun(thread))
	if n := s.count("SELECT COUNT(*) FROM memory_nodes WHERE thread_id = ? AND level = 0", thread); n != 2 {
		t.Fatalf("leaves after the first run = %d, want 2", n)
	}
	s.api.Push(reply1(anthropictest.Text("second answer")))
	status(t, s.post(fmt.Sprintf("/chat/%d/regenerate", thread), nil, hx, "true"), http.StatusOK)
	s.settle(s.lastRun(thread))
	if n := s.count("SELECT COUNT(*) FROM memory_nodes WHERE thread_id = ? AND level = 0", thread); n != 2 {
		t.Errorf("leaves after regenerate = %d, want 2", n)
	}
	if n := s.count("SELECT COUNT(*) FROM memory_nodes WHERE thread_id = ? AND src_from_seq = 2", thread); n != 0 {
		t.Errorf("superseded message 2 still has %d nodes", n)
	}
	r := s.post(fmt.Sprintf("/chat/%d/branch", thread), url.Values{"seq": {"1"}}, hx, "true")
	status(t, r, http.StatusSeeOther)
	to := r.Header.Get("Location")
	child, err := strconv.ParseInt(strings.TrimPrefix(to, "/chat/"), 10, 64)
	if err != nil {
		t.Fatalf("branch redirect %q", to)
	}
	if n := s.count("SELECT COUNT(*) FROM memory_nodes WHERE thread_id = ? AND level = 0", child); n != 1 {
		t.Errorf("child leaves = %d, want 1", n)
	}
	if n := s.count("SELECT COUNT(*) FROM memory_nodes WHERE thread_id = ? AND level = 0", thread); n != 2 {
		t.Errorf("source leaves after branch = %d, want 2", n)
	}
}

func TestMemoryTreeClipsLongLeaves(t *testing.T) {
	s := newSite(t)
	s.api.Push(reply1(anthropictest.Text(strings.Repeat("x", 600))))
	thread := s.startThread("hi")
	s.settle(s.lastRun(thread))
	var state string
	var bytes int
	if err := s.db.Read.QueryRow("SELECT state, bytes FROM memory_nodes WHERE thread_id = ? AND level = 0 AND src_from_seq = 2", thread).Scan(&state, &bytes); err != nil {
		t.Fatal(err)
	}
	if state != "pending" || bytes <= 512 {
		t.Errorf("long leaf state = %q bytes = %d, want pending over 512", state, bytes)
	}
	if n := s.count("SELECT COUNT(*) FROM memory_nodes WHERE thread_id = ? AND level = 0 AND state = 'direct'", thread); n != 1 {
		t.Errorf("direct leaves = %d, want 1", n)
	}
}

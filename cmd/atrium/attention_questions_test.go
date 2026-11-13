package main

import (
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
)

func TestAttentionListsParkedQuestionUntilAnswered(t *testing.T) {
	s := newAPISite(t)
	d := mustConnect(t, filepath.Join(s.dir, "atrium.db"), db.Key{})
	defer func() { _ = d.Close() }()
	if ran := runApp(t, "agent", "run", "--text", "warm"); ran.code != cli.ExitOK {
		t.Fatalf("warm run: %d %s", ran.code, ran.stderr)
	}
	s.api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("ask-1", "ask_operator", map[string]any{"question": "Which account should I use?"})))
	if ran := runApp(t, "agent", "run", "--text", "check my accounts"); ran.code != cli.ExitOK {
		t.Fatalf("ask run: %d %s", ran.code, ran.stderr)
	}
	var threadID int64
	if err := d.Read.QueryRow("SELECT thread_id FROM runs WHERE state = 'parked'").Scan(&threadID); err != nil {
		t.Fatalf("parked run: %v", err)
	}
	if got := operationAudits(t, d, "assistant.question.ask"); len(got) != 1 || !strings.HasPrefix(got[0], "ok thread:"+strconv.FormatInt(threadID, 10)+" ") {
		t.Errorf("question audits: %q", got)
	}
	attention := func() string {
		t.Helper()
		s.api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("a1", "attention", map[string]any{})))
		s.api.Push(anthropictest.Reply("end_turn", anthropictest.Text("done")))
		if ran := runApp(t, "agent", "run", "--kind", "cron", "--text", "attention"); ran.code != cli.ExitOK {
			t.Fatalf("attention run: %d %s", ran.code, ran.stderr)
		}
		var body string
		if err := d.Read.QueryRow("SELECT GROUP_CONCAT(content_json, '') FROM thread_messages WHERE thread_id = (SELECT MAX(id) FROM threads) AND role = 'user'").Scan(&body); err != nil {
			t.Fatal(err)
		}
		return body
	}
	if body := attention(); !strings.Contains(body, "Which account should I use?") || !strings.Contains(body, "Answer") {
		t.Fatalf("attention misses the parked question: %s", body)
	}
	s.api.Push(anthropictest.Reply("end_turn", anthropictest.Text("thanks")))
	answered := s.do(http.MethodPost, "/api/v1/threads/"+strconv.FormatInt(threadID, 10)+"/answer", s.token("owner"), map[string]any{"text": "personal"})
	if answered.status/100 != 2 {
		t.Fatalf("answer: %d %s", answered.status, answered.body)
	}
	for deadline := time.Now().Add(30 * time.Second); ; {
		var parked int
		if err := d.Read.QueryRow("SELECT COUNT(*) FROM runs WHERE thread_id = ? AND state IN ('parked', 'running', 'interrupted')", threadID).Scan(&parked); err != nil {
			t.Fatal(err)
		}
		if parked == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("answered run did not finish")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if body := attention(); strings.Contains(body, "Which account should I use?") {
		t.Fatalf("answered question is still listed: %s", body)
	}
}

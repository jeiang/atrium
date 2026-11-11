package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/cli"
)

func TestMemoryWorkSummarizesTheTree(t *testing.T) {
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	if ran := runApp(t, "agent", "run", "--text", "warm"); ran.code != cli.ExitOK {
		t.Fatalf("warm run: %d %s", ran.code, ran.stderr)
	}
	long := strings.Repeat("evidence ", 120)
	for i := range 4 {
		api.Push(anthropictest.Reply("end_turn", anthropictest.Text(long)))
		if ran := runApp(t, "agent", "run", "--thread", "1", "--text", fmt.Sprintf("message %d", i)); ran.code != cli.ExitOK {
			t.Fatalf("chat run %d: %d %s", i, ran.code, ran.stderr)
		}
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
	var before float64
	if err := d.Read.QueryRow("SELECT COALESCE(SUM(cost_usd), 0) FROM usage WHERE kind <> 'memory'").Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.Exec("UPDATE memory_jobs SET state = 'running', lease_until = 0 WHERE thread_id = 1"); err != nil {
		t.Fatal(err)
	}
	for range 60 {
		api.Push(anthropictest.Reply("end_turn", anthropictest.Text("summary")))
	}
	for range 10 {
		ran := runApp(t, "memory", "work", "1")
		if ran.code != cli.ExitOK {
			t.Fatalf("memory work: %d %s", ran.code, ran.stderr)
		}
		if strings.Contains(ran.stdout, ": 0 nodes") {
			break
		}
	}
	var pending, summarized, memoryRows int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM memory_nodes WHERE thread_id = 1 AND state = 'pending'").Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM memory_nodes WHERE thread_id = 1 AND state IN ('summary', 'fallback')").Scan(&summarized); err != nil {
		t.Fatal(err)
	}
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM usage WHERE kind = 'memory'").Scan(&memoryRows); err != nil {
		t.Fatal(err)
	}
	if pending != 0 {
		t.Errorf("%d nodes still pending", pending)
	}
	if summarized == 0 {
		t.Error("no summarized nodes")
	}
	if memoryRows == 0 {
		t.Error("no memory usage rows")
	}
	var chatCost, allCost float64
	if err := d.Read.QueryRow("SELECT COALESCE(SUM(cost_usd), 0) FROM usage WHERE kind <> 'memory'").Scan(&chatCost); err != nil {
		t.Fatal(err)
	}
	if err := d.Read.QueryRow("SELECT COALESCE(SUM(cost_usd), 0) FROM usage").Scan(&allCost); err != nil {
		t.Fatal(err)
	}
	if chatCost != before {
		t.Errorf("memory calls changed the non-memory spend: %v -> %v", before, chatCost)
	}
	if allCost < chatCost {
		t.Errorf("all spend %v below chat spend %v", allCost, chatCost)
	}
}

func TestMemoryWorkRetriesLongAnswersAndFallsBack(t *testing.T) {
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	if ran := runApp(t, "agent", "run", "--text", "warm"); ran.code != cli.ExitOK {
		t.Fatalf("warm run: %d %s", ran.code, ran.stderr)
	}
	long := strings.Repeat("evidence ", 120)
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text(long)))
	if ran := runApp(t, "agent", "run", "--thread", "1", "--text", "long"); ran.code != cli.ExitOK {
		t.Fatalf("chat run: %d %s", ran.code, ran.stderr)
	}
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text(strings.Repeat("too long ", 200))))
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("short")))
	api.Push(anthropictest.Reply("refusal", anthropictest.Text("")))
	for range 30 {
		api.Push(anthropictest.Reply("end_turn", anthropictest.Text("summary")))
	}
	for range 3 {
		if ran := runApp(t, "memory", "work", "1"); ran.code != cli.ExitOK {
			t.Fatalf("memory work: %d %s", ran.code, ran.stderr)
		}
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
	var short, fallback int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM memory_nodes WHERE thread_id = 1 AND state = 'summary' AND text = 'short'").Scan(&short); err != nil {
		t.Fatal(err)
	}
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM memory_nodes WHERE thread_id = 1 AND state = 'fallback'").Scan(&fallback); err != nil {
		t.Fatal(err)
	}
	if short == 0 {
		t.Error("the short retry answer was not kept")
	}
	if fallback == 0 {
		t.Error("the refusal did not store a fallback node")
	}
}

func TestMemoryWorkRefusesUnderTheRunCap(t *testing.T) {
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	if ran := runApp(t, "agent", "run", "--text", "warm"); ran.code != cli.ExitOK {
		t.Fatalf("warm run: %d %s", ran.code, ran.stderr)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
	if _, err := d.Write.Exec("INSERT INTO settings (key, value) VALUES ('max_run_cost_usd_haiku_5_5', '0.0001')"); err != nil {
		t.Fatal(err)
	}
	long := `[{"type":"text","text":"` + strings.Repeat("evidence ", 100) + `"}]`
	if _, err := d.Write.Exec("INSERT INTO thread_messages (thread_id, seq, role, content_json, created_at) VALUES (1, 3, 'user', ?, 0)", long); err != nil {
		t.Fatal(err)
	}
	if ran := runApp(t, "memory", "view", "1"); ran.code != cli.ExitOK {
		t.Fatalf("memory view: %d %s", ran.code, ran.stderr)
	}
	before := len(api.Requests())
	if ran := runApp(t, "memory", "work", "1"); ran.code != cli.ExitOK {
		t.Fatalf("memory work: %d %s", ran.code, ran.stderr)
	}
	if got := len(api.Requests()) - before; got != 0 {
		t.Errorf("summarizer sent %d requests under the run cap", got)
	}
	var state, errText string
	if err := d.Read.QueryRow("SELECT state, COALESCE(error, '') FROM memory_jobs WHERE thread_id = 1 ORDER BY id DESC LIMIT 1").Scan(&state, &errText); err != nil {
		t.Fatal(err)
	}
	if state != "failed" || !strings.Contains(errText, "run cap") {
		t.Errorf("job state %q error %q, want failed with a run cap error", state, errText)
	}
}

func TestMemoryWorkSummarizesPastTheClipPoint(t *testing.T) {
	_, api, _ := moduleFixture(t, flagsExcept("", ""))
	if ran := runApp(t, "agent", "run", "--text", "warm"); ran.code != cli.ExitOK {
		t.Fatalf("warm run: %d %s", ran.code, ran.stderr)
	}
	needle := "the hidden decision is to move the launch to Friday"
	long := strings.Repeat("filler ", 200) + needle
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("noted")))
	if ran := runApp(t, "agent", "run", "--thread", "1", "--text", long); ran.code != cli.ExitOK {
		t.Fatalf("long run: %d %s", ran.code, ran.stderr)
	}
	if ran := runApp(t, "memory", "view", "1"); ran.code != cli.ExitOK {
		t.Fatalf("memory view: %d %s", ran.code, ran.stderr)
	}
	for range 30 {
		api.Push(anthropictest.Reply("end_turn", anthropictest.Text("summary")))
	}
	for range 5 {
		ran := runApp(t, "memory", "work", "1")
		if ran.code != cli.ExitOK {
			t.Fatalf("memory work: %d %s", ran.code, ran.stderr)
		}
		if strings.Contains(ran.stdout, ": 0 nodes") {
			break
		}
	}
	found := false
	for _, request := range api.Requests() {
		body := string(request.Body)
		if strings.Contains(body, "Node source:") && strings.Contains(body, needle) {
			found = true
		}
	}
	if !found {
		t.Fatal("the summarizer did not receive the content past the clip point")
	}
}

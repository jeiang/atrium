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

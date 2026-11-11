package main

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/embed"
)

func TestKBSearchSkipsSemanticSearchWhenEmbeddingStalls(t *testing.T) {
	dir, fake, api, d := kbEmbedFixture(t)
	if ran := runApp(t, "agent", "run", "--text", "warm"); ran.code != cli.ExitOK {
		t.Fatalf("warm run: %d %s", ran.code, ran.stderr)
	}
	var stall atomic.Bool
	stall.Store(true)
	query := embed.QueryText("ledger")
	fake.gate = func(ctx context.Context, texts []string) {
		if stall.Load() && len(texts) == 1 && texts[0] == query {
			<-ctx.Done()
		}
	}

	base, _, stop := startServeStoppable(t, dir)
	w := asstBrowse(t, base)
	id := knowledgeCreate(t, w, url.Values{
		"title": {"Codes"}, "body": {"the ledger code is 7712"}, "scope": {"work"},
	})
	stop()

	_, _, stop = startServeStoppable(t, dir)
	waitFor(t, func() bool {
		_, vectors := recordHash(t, d, id)
		return vectors == 1
	})
	stop()

	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("s1", "kb_search", map[string]any{"query": "ledger"})))
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("the ledger code is 7712")))
	started := time.Now()
	ran := runAppLogged(t, "agent", "run", "--text", "what is the ledger code")
	elapsed := time.Since(started)
	if ran.code != cli.ExitOK {
		t.Fatalf("kb_search run: %d %s", ran.code, ran.stderr)
	}
	if elapsed > 20*time.Second {
		t.Fatalf("kb_search took %s with a stalled embedding endpoint", elapsed)
	}
	res := kbResult(t, d, latestThread(t, dir), "s1")
	if semantic, _ := res["semantic"].(bool); semantic {
		t.Errorf("stalled embedding still reported semantic results: %v", res)
	}
	hits, _ := res["results"].([]any)
	if len(hits) != 1 {
		t.Fatalf("stalled embedding returned %v, want the lexical record", res)
	}
	hit, _ := hits[0].(map[string]any)
	if hit["cite"] != fmt.Sprintf("K%d", id) {
		t.Errorf("lexical hit = %v, want K%d", hit, id)
	}
	if got := strings.Count(ran.stderr, "OPERATION=kb_search"); got != 1 {
		t.Errorf("skip logs for kb_search = %d, want 1: %s", got, ran.stderr)
	}
	if !strings.Contains(ran.stderr, "REASON=timeout") {
		t.Errorf("no timeout skip log: %s", ran.stderr)
	}

	stall.Store(false)
	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("s2", "kb_search", map[string]any{"query": "ledger"})))
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("the ledger code is 7712")))
	if ran := runApp(t, "agent", "run", "--text", "what is the ledger code"); ran.code != cli.ExitOK {
		t.Fatalf("vector kb_search run: %d %s", ran.code, ran.stderr)
	}
	res = kbResult(t, d, latestThread(t, dir), "s2")
	if semantic, _ := res["semantic"].(bool); !semantic {
		t.Errorf("successful vector search reported no semantic results: %v", res)
	}
	hits, _ = res["results"].([]any)
	if len(hits) != 1 {
		t.Fatalf("vector search returned %v, want the record", res)
	}
}

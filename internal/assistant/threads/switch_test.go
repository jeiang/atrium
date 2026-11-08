package threads

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/assistant/pricing"
	"github.com/jeiang/atrium/internal/db/dbtest"
)

func TestSwitchModelIsPricedWhileWarmAndDropsThinkingOnADownSwitch(t *testing.T) {
	d := dbtest.Schema(t)
	ctx := context.Background()
	now := 1_800_000_000.0
	id, err := Create(ctx, d.Write, now-120, Spec{Kind: Chat, Model: pricing.OpusID, Effort: pricing.Max, Thinking: "adaptive"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.Exec("INSERT INTO runs (id, thread_id, kind, model, effort, state, started_at) VALUES (1, ?, 'chat', ?, 'max', 'done', ?)", id, pricing.OpusID, now-60); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.Exec("INSERT INTO usage (ts, request_id, source, provider, thread_id, run_id, kind, model, effort, iterations, input_tokens, cache_write_5m, cache_write_1h, cache_read, output_tokens, searches, tier, cost_usd) VALUES (?, lower(hex(randomblob(16))), 'loop', 'anthropic', ?, 1, 'chat', ?, 'max', '[]', 1000, 0, 0, 500, 10, 0, 'flat', 0.1)", now-60, id, pricing.OpusID); err != nil {
		t.Fatal(err)
	}
	content := `[{"type":"thinking","thinking":"pondering"},{"type":"text","text":"hello"}]`
	if _, err := d.Write.Exec("INSERT INTO thread_messages (thread_id, seq, role, content_json, created_at) VALUES (?, 1, 'assistant', ?, ?)", id, content, now-60); err != nil {
		t.Fatal(err)
	}
	sw, err := SwitchModel(ctx, d.Write, id, pricing.SonnetID, "", "", now)
	if err != nil {
		t.Fatal(err)
	}
	if !sw.Changed || !sw.Warm || sw.CostUSD <= 0 || !sw.ThinkingDropped {
		t.Fatalf("switch = %+v, want changed, warm, priced and thinking dropped", sw)
	}
	var stored string
	if err := d.Read.QueryRow("SELECT content_json FROM thread_messages WHERE thread_id = ?", id).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	var blocks []map[string]any
	if err := json.Unmarshal([]byte(stored), &blocks); err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 1 || blocks[0]["type"] != "text" {
		t.Fatalf("content after the down-switch = %s", stored)
	}
	if _, err := d.Write.Exec("DELETE FROM usage WHERE thread_id = ?", id); err != nil {
		t.Fatal(err)
	}
	cold, err := SwitchModel(ctx, d.Write, id, pricing.OpusID, "", "", now)
	if err != nil {
		t.Fatal(err)
	}
	if cold.Warm || cold.CostUSD != 0 || cold.ThinkingDropped {
		t.Fatalf("cold switch = %+v, want cold, free and no drop", cold)
	}
	if cold.ThinkingAdjusted || cold.Changed != true {
		t.Fatalf("cold switch = %+v, want the model change applied", cold)
	}
	_ = assistant.DefaultContextLimit
}

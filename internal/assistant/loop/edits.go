package loop

import (
	"encoding/json"

	"github.com/jeiang/atrium/internal/anthropic"
	"github.com/jeiang/atrium/internal/assistant"
)

var editBetas = []string{anthropic.BetaContextManagement, anthropic.BetaCompaction}

func contextEdits(limit int64) json.RawMessage {
	t := assistant.TriggersFor(limit)
	tokens := func(n int64) map[string]any { return map[string]any{"type": "input_tokens", "value": n} }
	raw, err := json.Marshal(map[string]any{"edits": []any{
		map[string]any{
			"type": anthropic.EditClearToolUses, "trigger": tokens(t.Clear), "clear_at_least": tokens(t.ClearAtLeast),
			"keep": map[string]any{"type": "tool_uses", "value": assistant.KeepToolUses},
		},
		map[string]any{"type": anthropic.EditCompact, "trigger": tokens(t.Compact)},
	}})
	if err != nil {
		panic(err)
	}
	return raw
}

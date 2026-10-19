package service_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	actx "github.com/jeiang/atrium/internal/assistant/context"
	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/assistant/tool"
	"github.com/jeiang/atrium/internal/py"
)

var medium = strings.Repeat("m", 1500)

// contextTools stand in for the context_ops tools (#131) over the context package's operations.
func contextTools() []tool.Tool {
	guarded := func(v any, err error) (any, error) {
		var ce actx.ContextError
		if errors.As(err, &ce) {
			return nil, tool.Error(ce)
		}
		return v, err
	}
	return []tool.Tool{
		probe("medium", tool.Read, func(context.Context, *tool.Context, py.Dict) (any, error) { return medium, nil }),
		probe("context_status", tool.Read, func(ctx context.Context, tc *tool.Context, _ py.Dict) (any, error) {
			return guarded(actx.Status(ctx, tc.DB.Write, tc.ThreadID))
		}),
		probe("context_drop", tool.Read, func(ctx context.Context, tc *tool.Context, args py.Dict) (any, error) {
			ids, _ := args.Get("ids")
			return guarded(actx.Drop(ctx, tc.DB.Write, tc.ThreadID, tc.RunID, ids, tc.Now()))
		}),
	}
}

// resultOf is result_of: the tool_result block for toolUseID in a request body.
func resultOf(body map[string]any, toolUseID string) map[string]any {
	for _, m := range body["messages"].([]any) {
		for _, b := range path(m, "content").([]any) {
			if block := b.(map[string]any); block["type"] == "tool_result" && block["tool_use_id"] == toolUseID {
				return block
			}
		}
	}
	return nil
}

func (w *world) ledgerState(thread int64, item string) string {
	w.t.Helper()
	l, err := actx.LedgerRows(w.t.Context(), w.d.Read, thread)
	if err != nil {
		w.t.Fatal(err)
	}
	row, ok := l.Get(item)
	if !ok {
		return ""
	}
	return row.State
}

func dropped(t *testing.T, block map[string]any) bool {
	t.Helper()
	var v map[string]any
	if s, ok := block["content"].(string); !ok || json.Unmarshal([]byte(s), &v) != nil {
		return false
	}
	return v["dropped"] == true
}

// The Python test lowers context.COMMIT_SAVINGS to 100: a staged drop commits mid-run once it saves that
// much, but only while the current tool loop carries no signed thinking.
func TestCommitMidRunOnlyWithoutCurrentThinking(t *testing.T) {
	w := newWorld(t, contextTools()...)
	w.runner.CommitSavings = 100
	w.push(toolTurn(use("m1", "medium", nil)), reply(text("got it")))
	thread, _ := w.chat("first", threads.Chat)

	w.push(toolTurn(anthropictest.Thinking("t", "s"), use("d1", "context_drop", map[string]any{"ids": []string{"m1"}})), reply(text("later")))
	w.turn(thread, "second", threads.Chat)
	if got := resultOf(w.body(3), "m1"); got["content"] != medium {
		t.Errorf("m1 dropped under current thinking: %v", got["content"])
	}
	if s := w.ledgerState(thread, "m1"); s != actx.StagedDrop {
		t.Errorf("m1 ledger %q", s)
	}

	w.push(toolTurn(use("s1", "context_status", nil)), reply(text("now")))
	w.turn(thread, "third", threads.Chat)
	if !dropped(t, resultOf(w.body(4), "m1")) {
		t.Errorf("m1 not dropped at the next turn: %v", resultOf(w.body(4), "m1"))
	}
	if s := w.ledgerState(thread, "m1"); s != actx.Dropped {
		t.Errorf("m1 ledger %q", s)
	}

	w.push(toolTurn(use("m2", "medium", nil)), reply(text("x")))
	w.turn(thread, "fourth", threads.Chat)
	w.push(toolTurn(use("d2", "context_drop", map[string]any{"ids": []string{"m2"}})), reply(text("y")))
	w.turn(thread, "fifth", threads.Chat)
	if !dropped(t, resultOf(w.body(9), "m2")) {
		t.Errorf("m2 not dropped mid-run: %v", resultOf(w.body(9), "m2"))
	}
}

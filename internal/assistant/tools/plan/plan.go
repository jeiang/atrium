package plantools

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/jeiang/atrium/internal/assistant/progress"
	"github.com/jeiang/atrium/internal/assistant/tool"
	"github.com/jeiang/atrium/internal/assistant/workspace"
)

const Name = "plan"

func plan(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
	raw, _ := args.Get("items")
	var items []workspace.PlanItem
	err := tc.DB.WithTx(ctx, func(tx *sql.Tx) error {
		var err error
		if items, err = workspace.SetPlan(ctx, tx, tc.ThreadID, &tc.RunID, raw, tc.Now()); err != nil {
			return err
		}
		return progress.Emit(ctx, tx, tc.RunID, progress.Plan, map[string]any{"items": dicts(items)}, tc.Now())
	})
	var pe workspace.PlanError
	if errors.As(err, &pe) {
		return nil, tool.Error(pe)
	}
	if err != nil {
		return nil, err
	}
	done := 0
	for _, it := range items {
		if it.Done {
			done++
		}
	}
	return map[string]any{"items": dicts(items), "done": done, "total": len(items)}, nil
}

func dicts(items []workspace.PlanItem) []any {
	out := make([]any, len(items))
	for i, it := range items {
		out[i] = map[string]any{"text": it.Text, "done": it.Done}
	}
	return out
}

func Tools() []tool.Tool {
	return []tool.Tool{{
		Name:        Name,
		Description: "Set or replace this thread's checklist for a task that takes several steps, so the operator can follow progress. Call it once with the planned steps, then again with done flags as steps complete (always send the whole list).",
		InputSchema: json.RawMessage(`{"type": "object", "properties": {"items": {"type": "array", "maxItems": 20, "items": {"type": "object", "properties": {"text": {"type": "string", "maxLength": 200}, "done": {"type": "boolean"}}, "required": ["text"], "additionalProperties": false}}}, "required": ["items"], "additionalProperties": false}`),
		Handler:     plan,
		Effect:      tool.Read,
		Contexts:    tool.Kinds{tool.Chat, tool.Cron, tool.Nightly},
	}}
}

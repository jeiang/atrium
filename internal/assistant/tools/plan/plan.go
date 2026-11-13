package plantools

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jeiang/atrium/internal/application"
	asstapp "github.com/jeiang/atrium/internal/assistant/application"
	"github.com/jeiang/atrium/internal/tool"
)

const Name = "plan"

type Service func(*tool.Context) *asstapp.Service

func plan(svc Service) tool.Handler {
	return func(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
		raw, _ := args.Get("items")
		replaced, err := svc(tc).PlanReplace(ctx, asstapp.PlanReplaceInput{ThreadID: tc.ThreadID, RunID: tc.RunID, Items: raw})
		var refused *application.Error
		if errors.As(err, &refused) && refused.Code == application.Invalid {
			return nil, tool.Error(refused.Message)
		}
		if err != nil {
			return nil, err
		}
		items := make([]any, len(replaced.Data.Items))
		for i, it := range replaced.Data.Items {
			items[i] = map[string]any{"text": it.Text, "done": it.Done}
		}
		return map[string]any{"items": items, "done": replaced.Data.Done, "total": replaced.Data.Total}, nil
	}
}

func Tools(svc Service) []tool.Tool {
	return []tool.Tool{{
		Name:        Name,
		Description: "Set or replace this thread's checklist for a task that takes several steps, so the operator can follow progress. Call it once with the planned steps, then again with done flags as steps complete (always send the whole list).",
		InputSchema: json.RawMessage(`{"type": "object", "properties": {"items": {"type": "array", "maxItems": 20, "items": {"type": "object", "properties": {"text": {"type": "string", "maxLength": 200}, "done": {"type": "boolean"}}, "required": ["text"], "additionalProperties": false}}}, "required": ["items"], "additionalProperties": false}`),
		Handler:     plan(svc),
		Effect:      tool.Read,
		Annotated:   true,
		Contexts:    tool.Kinds{tool.Chat, tool.Cron, tool.Nightly},
	}}
}

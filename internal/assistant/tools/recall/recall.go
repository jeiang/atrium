package recalltools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jeiang/atrium/internal/application"
	asstapp "github.com/jeiang/atrium/internal/assistant/application"
	"github.com/jeiang/atrium/internal/tool"
)

type Service func(*tool.Context) *asstapp.Service

func recall(svc Service) tool.Handler {
	return func(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
		raw, _ := args.Get("tool_use_id")
		toolUseID, _ := raw.(string)
		toolUseID = strings.TrimSpace(toolUseID)
		resultID, _, err := tool.IntArg(args, "result_id", toolUseID == "")
		if err != nil {
			return nil, err
		}
		offset, err := tool.BoundedInt(args, "offset", 0, 1_000_000_000, 0)
		if err != nil {
			return nil, err
		}
		limit, err := asstapp.RecallLimit.Clamp(ctx, tc.DB.Read, args, "limit")
		if err != nil {
			return nil, err
		}
		got, err := svc(tc).ResultRecall(ctx, asstapp.RecallInput{ThreadID: tc.ThreadID, ResultID: resultID, ToolUseID: toolUseID, Offset: offset, Limit: limit})
		var e *application.Error
		if errors.As(err, &e) {
			return nil, tool.Error(e.Message)
		}
		if err != nil {
			return nil, err
		}
		var next any
		if got.Data.Next != nil {
			next = *got.Data.Next
		}
		out := map[string]any{"offset": offset, "total_chars": got.Data.Total, "text": got.Data.Text, "next_offset": next}
		if toolUseID != "" {
			out["tool_use_id"] = toolUseID
		} else {
			out["result_id"] = resultID
		}
		return out, nil
	}
}

func Tools(svc Service) []tool.Tool {
	return []tool.Tool{{
		Name: "recall",
		Description: "Page through a stored tool result: one replaced by a stub (pass its result_id) or one cleared from the context " +
			"(pass the tool_use_id of its call). Returns `limit` characters from `offset` and `next_offset` when more remain.",
		InputSchema: json.RawMessage(`{"type": "object", "properties": {"result_id": {"type": "integer"}, "tool_use_id": {"type": "string"}, "offset": {"type": "integer", "minimum": 0}, "limit": ` + asstapp.RecallLimit.Schema() + `}, "additionalProperties": false}`),
		Handler:     recall(svc),
		Effect:      tool.Read,
		Annotated:   true,
		Contexts:    tool.AllKinds,
	}}
}

package contexttools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/jeiang/atrium/internal/assistant"
	actx "github.com/jeiang/atrium/internal/assistant/context"
	"github.com/jeiang/atrium/internal/assistant/tool"
	"github.com/jeiang/atrium/internal/py"
)

var contexts = tool.Kinds{tool.Chat, tool.Cron, tool.Nightly}

func guarded(v any, err error) (any, error) {
	var ce actx.ContextError
	if errors.As(err, &ce) {
		return nil, tool.Error(ce)
	}
	return v, err
}

func idsOf(ctx context.Context, tc *tool.Context, args py.Dict) (any, error) {
	r := assistant.Read(ctx, tc.DB.Read)
	limit := r.Int(assistant.KeyContextMaxIDs)
	if err := r.Err(); err != nil {
		return nil, err
	}
	ids, _ := args.Get("ids")
	if list, ok := ids.([]any); ok && len(list) > limit {
		return nil, tool.Error(fmt.Sprintf("at most %d ids per call", limit))
	}
	return ids, nil
}

func status(ctx context.Context, tc *tool.Context, _ py.Dict) (any, error) {
	return guarded(actx.Status(ctx, tc.DB.Write, tc.ThreadID))
}

func pin(ctx context.Context, tc *tool.Context, args py.Dict) (any, error) {
	ids, err := idsOf(ctx, tc, args)
	if err != nil {
		return nil, err
	}
	return guarded(actx.Pin(ctx, tc.DB.Write, tc.ThreadID, tc.RunID, ids, tc.Now()))
}

func drop(ctx context.Context, tc *tool.Context, args py.Dict) (any, error) {
	ids, err := idsOf(ctx, tc, args)
	if err != nil {
		return nil, err
	}
	return guarded(actx.Drop(ctx, tc.DB.Write, tc.ThreadID, tc.RunID, ids, tc.Now()))
}

func fold(ctx context.Context, tc *tool.Context, args py.Dict) (any, error) {
	r := assistant.Read(ctx, tc.DB.Read)
	summaryChars := r.Int(assistant.KeyContextSummaryChars)
	maxSources := r.Int(assistant.KeyContextMaxSources)
	if err := r.Err(); err != nil {
		return nil, err
	}
	summary, _ := args.Get("summary")
	if s, ok := summary.(string); ok && utf8.RuneCountInString(s) > summaryChars {
		return nil, tool.Error(fmt.Sprintf("summary is limited to %d characters", summaryChars))
	}
	raw, _ := args.Get("source_ids")
	sources := []any{}
	if py.Truthy(raw) {
		list, ok := raw.([]any)
		if !ok || len(list) > maxSources {
			return nil, tool.Error(fmt.Sprintf("source_ids must be a list of at most %d ids", maxSources))
		}
		sources = list
	}
	fromSeq, _ := args.Get("from_seq")
	toSeq, _ := args.Get("to_seq")
	return guarded(actx.Fold(ctx, tc.DB.Write, tc.ThreadID, tc.RunID, fromSeq, toSeq, summary, sources, tc.Now()))
}

var idsSchema = json.RawMessage(`{"type": "object", "properties": {"ids": {"type": "array", "items": {"type": "string"}, "minItems": 1, "maxItems": 50}}, "required": ["ids"], "additionalProperties": false}`)

func Tools() []tool.Tool {
	return []tool.Tool{
		{
			Name:        "context_status",
			Description: "Dashboard of this conversation's context: approximate tokens, each tool result still in context (id, seq, tool, tokens, state: active/pinned/staged_drop/dropped) and folds. Use it before context_pin, context_drop or context_fold.",
			InputSchema: json.RawMessage(`{"type": "object", "properties": {}, "additionalProperties": false}`),
			Handler:     status,
			Effect:      tool.Read,
			Contexts:    contexts,
		},
		{
			Name:        "context_pin",
			Description: "Protect tool results (ids from context_status) from being dropped or folded by you. Pins do not override atrium's size limits.",
			InputSchema: idsSchema,
			Handler:     pin,
			Effect:      tool.Read,
			Contexts:    contexts,
		},
		{
			Name:        "context_drop",
			Description: "Stage tool results (ids from context_status) for removal from your working context once you no longer need them verbatim. Applied in batches at safe points; dropped results stay stored and can be paged back with recall(result_id).",
			InputSchema: idsSchema,
			Handler:     drop,
			Effect:      tool.Read,
			Contexts:    contexts,
		},
		{
			Name:        "context_fold",
			Description: "Replace a completed range of earlier messages (seq from_seq..to_seq, from context_status) with your own summary plus the exact source ids it rests on. from_seq must be an operator message and the range must end before the current operator message. Applied in batches at safe points; the originals stay stored.",
			InputSchema: json.RawMessage(`{"type": "object", "properties": {"from_seq": {"type": "integer", "minimum": 1}, "to_seq": {"type": "integer", "minimum": 1}, "summary": {"type": "string", "maxLength": 4000}, "source_ids": {"type": "array", "items": {"type": ["string", "integer"]}, "maxItems": 100}}, "required": ["from_seq", "to_seq", "summary"], "additionalProperties": false}`),
			Handler:     fold,
			Effect:      tool.Read,
			Contexts:    contexts,
		},
	}
}

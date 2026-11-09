package attentiontool

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jeiang/atrium/internal/attention"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/tool"
)

const schema = `{"type":"object","properties":{},"additionalProperties":false}`

func Tools() []tool.Tool {
	return []tool.Tool{{
		Name:        "attention",
		Description: "Read the attention view across the enabled queues: overdue, due soon, needs decision, waiting on them and upcoming.",
		InputSchema: json.RawMessage(schema),
		Effect:      tool.Read,
		Marks:       tool.PII | tool.Messages | tool.Untrusted,
		Annotated:   true,
		Contexts:    tool.Kinds{tool.Cron},
		Handler: func(ctx context.Context, tc *tool.Context, _ tool.Args) (any, error) {
			now := 0.0
			if tc.Now != nil {
				now = tc.Now()
			}
			set := module.SetFrom(ctx)
			if set == nil || set.Current() == nil {
				return nil, errors.New("attention: no module set")
			}
			result, err := attention.Query(ctx, tc.DB.Read, now, set.Current().Attention()...)
			if err != nil {
				return nil, err
			}
			raw, err := json.Marshal(result)
			if err != nil {
				return nil, err
			}
			var out any
			if err := json.Unmarshal(raw, &out); err != nil {
				return nil, err
			}
			if len(result.Items) == 0 {
				return tool.Marked{Value: out, Marks: 0}, nil
			}
			return tool.Marked{Value: out, Marks: tool.PII | tool.Messages | tool.Untrusted}, nil
		},
	}}
}

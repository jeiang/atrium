package attentiontool

import (
	"context"
	"encoding/json"

	attentionapp "github.com/jeiang/atrium/internal/attention/application"
	"github.com/jeiang/atrium/internal/tool"
)

const schema = `{"type":"object","properties":{},"additionalProperties":false}`

type Service func(*tool.Context) *attentionapp.Service

func Tools(svc Service) []tool.Tool {
	return []tool.Tool{{
		Name:        "attention",
		Description: "Read the attention view across the enabled queues: overdue, due soon, needs decision, waiting on them and upcoming.",
		InputSchema: json.RawMessage(schema),
		Effect:      tool.Read,
		Marks:       tool.PII | tool.Messages | tool.Untrusted,
		Annotated:   true,
		Contexts:    tool.Kinds{tool.Cron},
		Handler: func(ctx context.Context, tc *tool.Context, _ tool.Args) (any, error) {
			read, err := svc(tc).Read(ctx, attentionapp.ReadInput{})
			if err != nil {
				return nil, err
			}
			result := read.Data
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

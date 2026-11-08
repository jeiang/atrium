package attentiontool

import (
	"context"
	"encoding/json"

	"github.com/jeiang/atrium/internal/assistant/tool"
	"github.com/jeiang/atrium/internal/attention"
	attentionsources "github.com/jeiang/atrium/internal/attention/sources"
	"github.com/jeiang/atrium/internal/module"
)

const schema = `{"type":"object","properties":{},"additionalProperties":false}`

var Enabled = module.IsEnabled

func Tools() []tool.Tool {
	return []tool.Tool{{
		Name:        "attention",
		Description: "Read the attention view across the enabled queues: overdue, due soon, needs decision, waiting on them and upcoming.",
		InputSchema: json.RawMessage(schema),
		Effect:      tool.Read,
		Flags:       tool.Flags{PrivateData: true, UntrustedOutput: true},
		Annotated:   true,
		Contexts:    tool.Kinds{tool.Cron},
		Handler: func(ctx context.Context, tc *tool.Context, _ tool.Args) (any, error) {
			now := 0.0
			if tc.Now != nil {
				now = tc.Now()
			}
			result, err := attention.Query(ctx, tc.DB.Read, now, attentionsources.Enabled(ctx, Enabled)...)
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
			return out, nil
		},
	}}
}

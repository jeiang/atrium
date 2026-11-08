package askoperator

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/jeiang/atrium/internal/assistant/tool"
)

const Name = "ask_operator"

const Description = "Ask the operator one clarifying question and pause until they answer. " +
	"Use when a choice, permission or missing fact blocks progress and guessing would be wrong. " +
	"The run parks; the answer arrives as this tool's result."

func Tools() []tool.Tool {
	return []tool.Tool{{
		Name:        Name,
		Description: Description,
		InputSchema: json.RawMessage(`{"type":"object","properties":{"question":{"type":"string","maxLength":1000,"description":"One clear question for the operator."}},"required":["question"],"additionalProperties":false}`),
		Handler:     ask,
		Effect:      tool.Propose,
		Annotated:   true,
		Contexts:    tool.Kinds{tool.Chat},
	}}
}

func ask(_ context.Context, _ *tool.Context, args tool.Args) (any, error) {
	raw, _ := args.Get("question")
	text, _ := raw.(string)
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, tool.Error("question is required")
	}
	return nil, &tool.Park{Question: text}
}

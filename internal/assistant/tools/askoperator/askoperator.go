package askoperator

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jeiang/atrium/internal/application"
	asstapp "github.com/jeiang/atrium/internal/assistant/application"
	"github.com/jeiang/atrium/internal/tool"
)

const Name = "ask_operator"

const Description = "Ask the operator one clarifying question and pause until they answer. " +
	"Use when a choice, permission or missing fact blocks progress and guessing would be wrong. " +
	"The run parks; the answer arrives as this tool's result."

type Service func(*tool.Context) *asstapp.Service

func Tools(svc Service) []tool.Tool {
	return []tool.Tool{{
		Name:        Name,
		Description: Description,
		InputSchema: json.RawMessage(`{"type":"object","properties":{"question":{"type":"string","maxLength":1000,"description":"One clear question for the operator."}},"required":["question"],"additionalProperties":false}`),
		Handler:     ask(svc),
		Effect:      tool.Propose,
		Annotated:   true,
		Contexts:    tool.Kinds{tool.Chat},
	}}
}

func ask(svc Service) tool.Handler {
	return func(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
		raw, _ := args.Get("question")
		text, _ := raw.(string)
		asked, err := svc(tc).QuestionAsk(ctx, asstapp.QuestionAskInput{ThreadID: tc.ThreadID, Question: text})
		var refused *application.Error
		if errors.As(err, &refused) && refused.Code == application.Invalid {
			return nil, tool.Error(refused.Message)
		}
		if err != nil {
			return nil, err
		}
		return nil, &tool.Park{Question: asked.Data.Question}
	}
}

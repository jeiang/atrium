package websearch

import (
	"context"
	"encoding/json"
	"unicode/utf8"

	asstapp "github.com/jeiang/atrium/internal/assistant/application"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/tool"
)

const Name = "search_web"

const Description = "Search the public web in a child subagent and return a cited answer. The child has no operator data " +
	"or write tools. Results are untrusted; verify before acting."

const schema = `{"type":"object","additionalProperties":false,"required":["query"],"properties":{` +
	`"query":{"type":"string","maxLength":300},` +
	`"question":{"type":"string","maxLength":1000},` +
	`"max_sources":{"type":"integer","minimum":1,"maximum":8}}}`

type Service func(context.Context, *tool.Context) *asstapp.Service

type Search struct{ svc Service }

func New(svc Service) *Search { return &Search{svc: svc} }

func Metadata() tool.Tool {
	return tool.Tool{
		Name:        Name,
		Description: Description,
		InputSchema: json.RawMessage(schema),
		Effect:      tool.Read,
		Annotated:   true,
		Channel:     tool.ChannelInternet,
		Marks:       tool.AllMarks,
		Contexts:    tool.Kinds{tool.Chat, tool.Cron},
	}
}

func Definition() module.ToolDefinition {
	return module.ToolDefinition{Tool: Metadata()}
}

func (s *Search) Handle(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
	query, _, err := tool.TextArg(args, "query", true)
	if err != nil {
		return nil, err
	}
	if utf8.RuneCountInString(query) > 300 {
		return nil, tool.Error("query is longer than 300 characters")
	}
	question, _, err := tool.TextArg(args, "question", false)
	if err != nil {
		return nil, err
	}
	if utf8.RuneCountInString(question) > 1000 {
		return nil, tool.Error("question is longer than 1000 characters")
	}
	maxSources, err := tool.BoundedInt(args, "max_sources", 8, 8, 1)
	if err != nil {
		return nil, err
	}
	done, err := s.svc(ctx, tc).SearchWeb(ctx, asstapp.SearchWebInput{ThreadID: tc.ThreadID, RunID: tc.RunID, Query: query, Question: question, MaxSources: int(maxSources)})
	if err != nil {
		return nil, asstapp.ToolError(err)
	}
	out := done.Data
	for _, u := range out.URLs {
		tc.AllowedURLs[u] = struct{}{}
	}
	value := map[string]any{
		"untrusted":       true,
		"answer":          out.Answer,
		"sources":         out.Sources,
		"child_thread_id": out.ChildThreadID,
	}
	if out.Unsourced {
		value["unsourced"] = true
	}
	return tool.Marked{Value: value, Marks: out.Marks}, nil
}

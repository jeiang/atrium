package memorytools

import (
	"context"
	"encoding/json"
	"fmt"

	asstapp "github.com/jeiang/atrium/internal/assistant/application"
	"github.com/jeiang/atrium/internal/tool"
)

const searchName = "search_threads"

const searchDescription = "Search the memory summaries of every thread for a fact, returning the thread, node range and summary line."

const searchSchema = `{"type":"object","properties":{"query":{"type":"string","maxLength":500},` +
	`"limit":{"type":"integer","minimum":1,"maximum":50},` +
	`"project":{"type":"integer","description":"Only hits in this project id."}},` +
	`"required":["query"],"additionalProperties":false}`

func searchTools(svc Service) []tool.Tool {
	return []tool.Tool{{
		Name: searchName, Description: searchDescription, InputSchema: json.RawMessage(searchSchema), Handler: searchThreads(svc),
		Effect: tool.Read, Marks: tool.AllMarks, Annotated: true,
		Contexts: tool.Kinds{tool.Chat, tool.Cron, tool.Nightly, tool.Sub},
	}}
}

func searchThreads(svc Service) tool.Handler {
	return func(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
		raw, _ := args.Get("query")
		query, _ := raw.(string)
		limit, err := tool.BoundedInt(args, "limit", 10, 50, 1)
		if err != nil {
			return nil, err
		}
		project, err := tool.BoundedInt(args, "project", 0, 1<<40, 0)
		if err != nil {
			return nil, err
		}
		got, err := svc(tc).MemorySearch(ctx, asstapp.MemorySearchInput{ThreadID: tc.ThreadID, Query: query, Project: project, Limit: int(limit)})
		if err != nil {
			return nil, fail(err)
		}
		hits := make([]any, len(got.Data.Hits))
		for i, h := range got.Data.Hits {
			var titleValue any
			if h.Title != nil {
				titleValue = *h.Title
			}
			hit := map[string]any{"thread_id": h.ThreadID, "title": titleValue, "kind": h.Kind, "project_id": h.ProjectID, "from": h.FromSeq, "to": h.ToSeq, "state": h.State, "text": h.Text, "at": h.CreatedAt}
			if h.Own {
				hit["zoom"] = map[string]any{"id": h.FromSeq, "n": h.ToSeq - h.FromSeq + 1}
			} else {
				hit["chat"] = fmt.Sprintf("/chat/%d", h.ThreadID)
			}
			hits[i] = hit
		}
		return tool.Marked{Value: map[string]any{"hits": hits}, Marks: got.Data.Marks}, nil
	}
}

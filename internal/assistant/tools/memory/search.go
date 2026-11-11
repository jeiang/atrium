package memorytools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/assistant/memory"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/tool"
)

const searchName = "search_threads"

const searchDescription = "Search the memory summaries of every thread for a fact, returning the thread, node range and summary line."

const searchSchema = `{"type":"object","properties":{"query":{"type":"string","maxLength":500},` +
	`"limit":{"type":"integer","minimum":1,"maximum":50},` +
	`"project":{"type":"integer","description":"Only hits in this project id."}},` +
	`"required":["query"],"additionalProperties":false}`

func searchTools() []tool.Tool {
	return []tool.Tool{{
		Name: searchName, Description: searchDescription, InputSchema: json.RawMessage(searchSchema), Handler: searchThreads,
		Effect: tool.Read, Marks: tool.AllMarks, Annotated: true,
		Contexts: tool.Kinds{tool.Chat, tool.Cron, tool.Nightly, tool.Sub},
	}}
}

func searchThreads(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
	raw, _ := args.Get("query")
	query, _ := raw.(string)
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, tool.Error("query is required")
	}
	limit, err := tool.BoundedInt(args, "limit", 10, 50, 1)
	if err != nil {
		return nil, err
	}
	project, err := tool.BoundedInt(args, "project", 0, 1<<40, 0)
	if err != nil {
		return nil, err
	}
	found, err := memory.Search(ctx, tc.DB.Read, tc.ThreadID, memory.SearchFilter{Query: query, Project: project, Limit: int(limit)})
	if err != nil {
		return nil, err
	}
	page := int64(assistant.TuningAgentRecallPageChars)
	var hits []any
	var marks tool.Marks
	seen := map[[3]int64]bool{}
	for _, h := range found {
		key := [3]int64{h.ThreadID, h.FromSeq, h.ToSeq}
		if seen[key] {
			continue
		}
		seen[key] = true
		var titleValue any
		if h.Title != nil {
			titleValue = *h.Title
		}
		hit := map[string]any{"thread_id": h.ThreadID, "title": titleValue, "kind": h.Kind, "project_id": h.ProjectID, "from": h.FromSeq, "to": h.ToSeq, "state": h.State, "text": clip(h.Text, int(page)), "at": h.CreatedAt}
		if h.ThreadID == tc.ThreadID {
			hit["zoom"] = map[string]any{"id": h.FromSeq, "n": h.ToSeq - h.FromSeq + 1}
		} else {
			hit["chat"] = fmt.Sprintf("/chat/%d", h.ThreadID)
		}
		hits = append(hits, hit)
		marks |= h.Marks
	}
	if hits == nil {
		hits = []any{}
	}
	return tool.Marked{Value: map[string]any{"hits": hits}, Marks: tool.Messages | marks}, nil
}

func threadMarks(ctx context.Context, q db.Querier, ids []int64) (tool.Marks, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	list := make([]any, len(ids))
	for i, id := range ids {
		list[i] = id
	}
	encoded, err := json.Marshal(list)
	if err != nil {
		return 0, err
	}
	rows, err := q.QueryContext(ctx, "SELECT marks FROM threads WHERE id IN (SELECT value FROM json_each(?))", encoded)
	if err != nil {
		return 0, err
	}
	defer func() { _ = rows.Close() }()
	var out tool.Marks
	for rows.Next() {
		var marks int64
		if err := rows.Scan(&marks); err != nil {
			return 0, err
		}
		out |= tool.Marks(marks)
	}
	return out, rows.Err()
}

func clip(text string, limit int) string {
	if limit <= 0 || len(text) <= limit {
		return text
	}
	return text[:limit] + "…"
}

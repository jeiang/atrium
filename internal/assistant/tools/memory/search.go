package memorytools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/assistant/memory"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/textsearch"
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
	match, err := textsearch.FTSQuery(query)
	if err != nil {
		return nil, tool.Error("invalid query")
	}
	sql := "SELECT n.thread_id, t.title, t.kind, COALESCE(t.project_id, 0), n.level, n.idx, n.src_from_seq, n.src_to_seq, n.state, n.text, n.created_at " +
		"FROM memory_fts f JOIN memory_nodes n ON n.thread_id = f.thread_id AND n.level = f.level AND n.idx = f.idx " +
		"JOIN threads t ON t.id = n.thread_id WHERE memory_fts MATCH ? AND n.state IN (?, ?, ?)"
	params := []any{match, memory.Summary, memory.Fallback, memory.Direct}
	if project != 0 {
		sql += " AND t.project_id = ?"
		params = append(params, project)
	}
	sql += " ORDER BY rank LIMIT ?"
	params = append(params, limit)
	rows, err := tc.DB.Read.QueryContext(ctx, sql, params...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	page := int64(assistant.TuningAgentRecallPageChars)
	var hits []any
	var ids []int64
	seen := map[[3]int64]bool{}
	for rows.Next() {
		var threadID, projectID, from, to int64
		var title *string
		var kind, state, text string
		var level, idx int
		var created float64
		if err := rows.Scan(&threadID, &title, &kind, &projectID, &level, &idx, &from, &to, &state, &text, &created); err != nil {
			return nil, err
		}
		key := [3]int64{threadID, from, to}
		if seen[key] {
			continue
		}
		seen[key] = true
		ids = append(ids, threadID)
		var titleValue any
		if title != nil {
			titleValue = *title
		}
		hit := map[string]any{"thread_id": threadID, "title": titleValue, "kind": kind, "project_id": projectID, "from": from, "to": to, "state": state, "text": clip(text, int(page)), "at": created}
		if threadID == tc.ThreadID {
			hit["zoom"] = map[string]any{"id": from, "n": to - from + 1}
		} else {
			hit["chat"] = fmt.Sprintf("/chat/%d", threadID)
		}
		hits = append(hits, hit)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if hits == nil {
		hits = []any{}
	}
	marks, err := threadMarks(ctx, tc.DB.Read, ids)
	if err != nil {
		return nil, err
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

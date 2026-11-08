package recalltools

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/assistant/tool"
)

func slice(s string, offset, limit int64) string {
	start, end := len(s), len(s)
	var n int64
	for i := range s {
		if n == offset {
			start = i
		}
		if n == offset+limit {
			end = i
			break
		}
		n++
	}
	if start > end {
		return ""
	}
	return s[start:end]
}

var recallLimitBound = tool.NewBound(assistant.KeyRecallMaxPageChars, assistant.KeyRecallPageChars)

func recall(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
	resultID, _, err := tool.IntArg(args, "result_id", true)
	if err != nil {
		return nil, err
	}
	offset, err := tool.BoundedInt(args, "offset", 0, 1_000_000_000, 0)
	if err != nil {
		return nil, err
	}
	limit, err := recallLimitBound.Clamp(ctx, tc.DB.Read, args, "limit")
	if err != nil {
		return nil, err
	}
	var body string
	err = tc.DB.Read.QueryRowContext(ctx,
		"SELECT r.body FROM tool_results r JOIN runs ON runs.id = r.run_id WHERE r.result_id = ? AND runs.thread_id = ?",
		resultID, tc.ThreadID).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, tool.Error(fmt.Sprintf("no stored result %d in this thread", resultID))
	}
	if err != nil {
		return nil, err
	}
	text := slice(body, offset, limit)
	total := int64(utf8.RuneCountInString(body))
	end := offset + int64(utf8.RuneCountInString(text))
	var next any
	if end < total {
		next = end
	}
	return map[string]any{"result_id": resultID, "offset": offset, "total_chars": total, "text": text, "next_offset": next}, nil
}

func Tools() []tool.Tool {
	return []tool.Tool{{
		Name:        "recall",
		Description: "Page through a stored tool result that was replaced by a stub. Returns `limit` characters from `offset` and `next_offset` when more remain.",
		InputSchema: json.RawMessage(`{"type": "object", "properties": {"result_id": {"type": "integer"}, "offset": {"type": "integer", "minimum": 0}, "limit": ` + recallLimitBound.Schema() + `}, "required": ["result_id"], "additionalProperties": false}`),
		Handler:     recall,
		Effect:      tool.Read,
		Annotated:   true,
		Flags:       tool.Flags{PrivateData: true},
		Contexts:    tool.AllKinds,
	}}
}

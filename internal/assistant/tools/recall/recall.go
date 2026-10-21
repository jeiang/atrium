package recalltools

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"unicode/utf8"

	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/assistant/tool"
	"github.com/jeiang/atrium/internal/py"
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

func recall(ctx context.Context, tc *tool.Context, args py.Dict) (any, error) {
	raw, _ := args.Get("result_id")
	var resultID int64
	switch v := raw.(type) {
	case int64:
		resultID = v
	case *big.Int:
		return nil, &tool.Raised{Class: "OverflowError"}
	default:
		return nil, tool.Error("result_id must be an integer")
	}
	offset, err := tool.BoundedInt(args, "offset", 0, 1_000_000_000, 0)
	if err != nil {
		return nil, err
	}
	r := assistant.Read(ctx, tc.DB.Read)
	pageChars := r.Int(assistant.KeyRecallPageChars)
	maxPageChars := r.Int(assistant.KeyRecallMaxPageChars)
	if err := r.Err(); err != nil {
		return nil, err
	}
	limit, err := tool.BoundedInt(args, "limit", int64(pageChars), int64(maxPageChars), 1)
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
	return py.Dict{
		{Key: "result_id", Value: resultID},
		{Key: "offset", Value: offset},
		{Key: "total_chars", Value: total},
		{Key: "text", Value: text},
		{Key: "next_offset", Value: next},
	}, nil
}

func Tools() []tool.Tool {
	return []tool.Tool{{
		Name:        "recall",
		Description: "Page through a stored tool result that was replaced by a stub. Returns `limit` characters from `offset` and `next_offset` when more remain.",
		InputSchema: json.RawMessage(`{"type": "object", "properties": {"result_id": {"type": "integer"}, "offset": {"type": "integer", "minimum": 0}, "limit": {"type": "integer", "minimum": 1, "maximum": 16000}}, "required": ["result_id"], "additionalProperties": false}`),
		Handler:     recall,
		Effect:      tool.Read,
		Contexts:    tool.AllKinds,
	}}
}

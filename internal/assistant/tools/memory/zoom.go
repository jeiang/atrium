package memorytools

import (
	"context"
	"encoding/json"
	"fmt"
	"math/bits"
	"strings"
	"unicode/utf8"

	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/assistant/block"
	"github.com/jeiang/atrium/internal/assistant/journal"
	"github.com/jeiang/atrium/internal/assistant/memory"
	"github.com/jeiang/atrium/internal/tool"
)

const Name = "zoom"

const Description = "Open a memory line when its summary is not enough: a summary's two children, or one message's source."

const schema = `{"type":"object","properties":{"id":{"type":"integer","description":"Start message ordinal."},` +
	`"n":{"type":"integer","minimum":1,"description":"Covered messages; a power of two."},` +
	`"at":{"type":"integer","minimum":0,"description":"Character offset for a leaf page."}},` +
	`"required":["id"],"additionalProperties":false}`

func Tools() []tool.Tool {
	return append([]tool.Tool{{
		Name: Name, Description: Description, InputSchema: json.RawMessage(schema), Handler: zoom,
		Effect: tool.Read, Marks: tool.Messages, Annotated: true,
		Contexts: tool.Kinds{tool.Chat, tool.Cron, tool.Nightly, tool.Sub},
	}}, searchTools()...)
}

func zoom(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
	id, _, err := tool.IntArg(args, "id", true)
	if err != nil {
		return nil, err
	}
	n, err := tool.BoundedInt(args, "n", 1, 1<<20, 1)
	if err != nil {
		return nil, err
	}
	at, err := tool.BoundedInt(args, "at", 0, 1_000_000_000, 0)
	if err != nil {
		return nil, err
	}
	if n&(n-1) != 0 {
		return nil, rootsError(ctx, tc, id, n)
	}
	nodes, err := memory.Tree(ctx, tc.DB.Read, tc.ThreadID)
	if err != nil {
		return nil, err
	}
	if n == 1 {
		return leaf(ctx, tc, nodes, id, at)
	}
	level := bits.TrailingZeros64(uint64(n))
	var parent *memory.Node
	for i := range nodes {
		if nodes[i].Level == level && nodes[i].FromSeq == id && nodes[i].ToSeq == id+n-1 {
			parent = &nodes[i]
			break
		}
	}
	if parent == nil {
		return nil, rootsError(ctx, tc, id, n)
	}
	var children []any
	for i := range nodes {
		if nodes[i].Level == level-1 && (nodes[i].Index == parent.Index*2 || nodes[i].Index == parent.Index*2+1) {
			children = append(children, map[string]any{"id": nodes[i].FromSeq, "n": n / 2, "to": nodes[i].ToSeq, "state": nodes[i].State, "text": nodes[i].Text})
		}
	}
	if len(children) != 2 {
		return nil, rootsError(ctx, tc, id, n)
	}
	return map[string]any{"id": id, "n": n, "children": children}, nil
}

func leaf(ctx context.Context, tc *tool.Context, nodes []memory.Node, id, at int64) (any, error) {
	var found *memory.Node
	for i := range nodes {
		if nodes[i].Level == 0 && nodes[i].FromSeq == id {
			found = &nodes[i]
			break
		}
	}
	if found == nil {
		return nil, rootsError(ctx, tc, id, 1)
	}
	if len(found.ResultIDs) > 0 {
		return map[string]any{
			"id": id, "n": 1, "result_id": found.ResultIDs[0], "use": "recall",
			"text": fmt.Sprintf("tool result %d; page it with recall", found.ResultIDs[0]),
		}, nil
	}
	messages, err := journal.MessagesOf(ctx, tc.DB.Read, tc.ThreadID)
	if err != nil {
		return nil, err
	}
	var body string
	for _, m := range messages {
		if m.Seq != id {
			continue
		}
		blocks, err := block.DecodeAll(m.Content)
		if err != nil {
			return nil, err
		}
		var parts []string
		for _, b := range blocks {
			if b.Kind == block.Text {
				parts = append(parts, b.Text())
			}
		}
		body = strings.Join(parts, "\n")
		break
	}
	limit := int64(assistant.Read(ctx, tc.DB.Read).Int(assistant.KeyRecallPageChars))
	text := slice(body, at, limit)
	total := int64(utf8.RuneCountInString(body))
	end := at + int64(utf8.RuneCountInString(text))
	var next any
	if end < total {
		next = end
	}
	return map[string]any{"id": id, "n": 1, "offset": at, "total_chars": total, "text": text, "next_offset": next}, nil
}

func rootsError(ctx context.Context, tc *tool.Context, id, n int64) error {
	nodes, err := memory.Tree(ctx, tc.DB.Read, tc.ThreadID)
	if err != nil {
		return err
	}
	maxLevel := -1
	for _, node := range nodes {
		if node.Level > maxLevel {
			maxLevel = node.Level
		}
	}
	var roots []string
	for _, node := range nodes {
		if node.Level == maxLevel {
			roots = append(roots, fmt.Sprintf("%d..%d %s", node.FromSeq, node.ToSeq, node.State))
		}
	}
	if len(roots) == 0 {
		roots = []string{"none"}
	}
	return tool.Error(fmt.Sprintf("invalid zoom range %d..%d; roots: %s", id, id+n-1, strings.Join(roots, ", ")))
}

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

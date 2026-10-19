package proposetools

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"

	"github.com/jeiang/atrium/internal/assistant/tool"
	"github.com/jeiang/atrium/internal/py"
)

const Name = "propose"

type Builder func(ctx context.Context, tc *tool.Context, payload py.Dict) (*int64, error)

var (
	mu    sync.RWMutex
	kinds = map[string]Builder{}
)

func Register(kind string, fn Builder) {
	mu.Lock()
	defer mu.Unlock()
	kinds[kind] = fn
}

func Kinds() []string {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]string, 0, len(kinds))
	for k := range kinds {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func builder(kind string) Builder {
	mu.RLock()
	defer mu.RUnlock()
	return kinds[kind]
}

func propose(ctx context.Context, tc *tool.Context, args py.Dict) (any, error) {
	kind, ok, err := tool.TextArg(args, "kind", true)
	if err != nil {
		return nil, err
	}
	payload, _ := args.Get("payload")
	shown := "None"
	var fn Builder
	if ok {
		shown = py.Repr(kind)
		fn = builder(kind)
	}
	if fn == nil {
		return nil, tool.Error("unknown proposal kind " + shown + "; allowed: " + strings.Join(Kinds(), ", "))
	}
	dict, isObject := payload.(py.Dict)
	if !isObject {
		return nil, tool.Error("payload must be an object")
	}
	id, err := fn(ctx, tc, dict)
	if err != nil {
		return nil, err
	}
	if id == nil {
		return py.Dict{{Key: "proposal_id", Value: nil}, {Key: "note", Value: "an identical proposal already exists"}}, nil
	}
	return py.Dict{{Key: "proposal_id", Value: *id}, {Key: "status", Value: "pending"}, {Key: "note", Value: "the operator approves it on the Proposals page"}}, nil
}

func Tools() []tool.Tool {
	return []tool.Tool{{
		Name:        Name,
		Description: "Create a pending proposal for the operator to review; nothing is applied until they approve it. Kinds: notion_edit {page_id, instruction}: propose an edit to a Notion page for the operator to approve; notion_link {source_id, target_id}: propose linking two related Notion pages",
		InputSchema: json.RawMessage(`{"type": "object", "properties": {"kind": {"type": "string", "enum": ["notion_edit", "notion_link"]}, "payload": {"type": "object"}}, "required": ["kind", "payload"], "additionalProperties": false}`),
		Handler:     propose,
		Effect:      tool.Propose,
		Contexts:    tool.Kinds{tool.Chat, tool.Cron},
	}}
}

package proposetools

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/jeiang/atrium/internal/assistant/tool"
)

const Name = "propose"

type Builder struct {
	Description string
	Build       func(ctx context.Context, tc *tool.Context, payload map[string]any) (*int64, error)
}

var (
	mu    sync.RWMutex
	kinds = map[string]Builder{}
)

func Register(kind, description string, build func(ctx context.Context, tc *tool.Context, payload map[string]any) (*int64, error)) {
	mu.Lock()
	defer mu.Unlock()
	kinds[kind] = Builder{Description: description, Build: build}
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

func describe() string {
	mu.RLock()
	defer mu.RUnlock()
	parts := make([]string, 0, len(kinds))
	for k, b := range kinds {
		parts = append(parts, k+" "+b.Description)
	}
	slices.Sort(parts)
	return strings.Join(parts, "; ")
}

func propose(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
	kind, ok, err := tool.TextArg(args, "kind", true)
	if err != nil {
		return nil, err
	}
	payload, _ := args.Get("payload")
	shown := "unknown"
	var fn Builder
	if ok {
		shown = strconv.Quote(kind)
		mu.RLock()
		fn = kinds[kind]
		mu.RUnlock()
	}
	if fn.Build == nil {
		return nil, tool.Error("unknown proposal kind " + shown + "; allowed: " + strings.Join(Kinds(), ", "))
	}
	dict, isObject := payload.(map[string]any)
	if !isObject {
		return nil, tool.Error("payload must be an object")
	}
	id, err := fn.Build(ctx, tc, dict)
	if err != nil {
		return nil, err
	}
	if id == nil {
		return map[string]any{"proposal_id": nil, "note": "an identical proposal already exists"}, nil
	}
	return map[string]any{"proposal_id": *id, "status": "pending", "note": "the operator approves it on the Proposals page"}, nil
}

func Tools() []tool.Tool {
	allowed, _ := json.Marshal(Kinds())
	return []tool.Tool{{
		Name:        Name,
		Description: "Create a pending proposal for the operator to review; nothing is applied until they approve it. Kinds: " + describe(),
		InputSchema: json.RawMessage(fmt.Sprintf(`{"type": "object", "properties": {"kind": {"type": "string", "enum": %s}, "payload": {"type": "object"}}, "required": ["kind", "payload"], "additionalProperties": false}`, allowed)),
		Handler:     propose,
		Effect:      tool.Propose,
		Annotated:   true,
		Contexts:    tool.Kinds{tool.Chat, tool.Cron},
	}}
}

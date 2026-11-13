package proposetools

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	asstapp "github.com/jeiang/atrium/internal/assistant/application"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/proposals"
	"github.com/jeiang/atrium/internal/tool"
)

const Name = "propose"

func definition(kinds []proposals.Definition) tool.Tool {
	builders := make(map[string]*proposals.Builder, len(kinds))
	names := make([]string, 0, len(kinds))
	var parts []string
	for _, d := range kinds {
		if d.Build != nil {
			builders[d.Kind.Name] = d.Build
			names = append(names, d.Kind.Name)
			parts = append(parts, d.Kind.Name+" "+d.Build.Description)
		}
	}
	slices.Sort(names)
	slices.Sort(parts)
	allowed, _ := json.Marshal(names)
	return tool.Tool{
		Name:        Name,
		Description: "Create a pending proposal for the operator to review; nothing is applied until they approve it. Kinds: " + strings.Join(parts, "; "),
		InputSchema: json.RawMessage(fmt.Sprintf(`{"type": "object", "properties": {"kind": {"type": "string", "enum": %s}, "payload": {"type": "object"}}, "required": ["kind", "payload"], "additionalProperties": false}`, allowed)),
		Handler: func(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
			kind, ok, err := tool.TextArg(args, "kind", true)
			if err != nil {
				return nil, err
			}
			payload, _ := args.Get("payload")
			out, err := asstapp.New(tc.DB, tc.Env, tc.Now).WithProposalBuilders(tc, builders, names).Propose(ctx, asstapp.ProposeInput{Kind: kind, Present: ok, Payload: payload})
			if err != nil {
				return nil, err
			}
			if out.Data.ProposalID == nil {
				return map[string]any{"proposal_id": nil, "note": out.Data.Note}, nil
			}
			return map[string]any{"proposal_id": *out.Data.ProposalID, "status": out.Data.Status, "note": out.Data.Note}, nil
		},
		Effect: tool.Propose, Annotated: true, Contexts: tool.Kinds{tool.Chat, tool.Cron},
	}
}

func Definition(kinds []proposals.Definition) module.ToolDefinition {
	return module.ToolDefinition{Tool: definition(kinds), Bind: func(ctx context.Context, _ db.Querier, _ tool.Tool) (tool.Tool, bool, error) {
		var enabled []proposals.Definition
		for _, d := range kinds {
			if module.IsEnabled(ctx, d.Kind.Module) {
				enabled = append(enabled, d)
			}
		}
		return definition(enabled), true, nil
	}}
}

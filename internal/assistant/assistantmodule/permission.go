package assistantmodule

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jeiang/atrium/internal/assistant/permission"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/proposals"
)

func permissionProposals(resume func(context.Context, int64) error) []proposals.Definition {
	if resume == nil {
		return nil
	}
	return []proposals.Definition{{
		Kind: proposals.Kind{Name: permission.Kind, Module: module.Assistant, Label: "Permission request", Icon: "lock", Approval: true},
		Execute: func(ctx context.Context, p *proposals.Proposal, _ any) (any, error) {
			var payload permission.Payload
			if err := json.Unmarshal(p.Payload, &payload); err != nil {
				return nil, err
			}
			if err := resume(ctx, payload.RunID); err != nil {
				return nil, err
			}
			return map[string]any{"summary": "Permission request approved"}, nil
		},
		Dismiss: func(ctx context.Context, p *proposals.Proposal) error {
			var payload permission.Payload
			if err := json.Unmarshal(p.Payload, &payload); err != nil {
				return err
			}
			err := resume(ctx, payload.RunID)
			var disabled *module.OwnerDisabled
			if errors.As(err, &disabled) && disabled.Module == module.Assistant {
				return nil
			}
			return err
		},
	}}
}

package assistantmodule

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jeiang/atrium/internal/assistant/permission"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/proposals"
	"github.com/jeiang/atrium/internal/unit"
)

func permissionProposals(d *db.DB, resume func(context.Context, int64) error) []proposals.Definition {
	if resume == nil {
		return nil
	}
	last := func(ctx context.Context, p *proposals.Proposal) (int64, bool, error) {
		var payload permission.Payload
		if err := json.Unmarshal(p.Payload, &payload); err != nil {
			return 0, false, err
		}
		var open int
		if err := d.Read.QueryRowContext(ctx, "SELECT COUNT(*) FROM proposals WHERE kind = ? AND status = ? AND id <> ? "+
			"AND json_extract(payload, '$.run_id') = ?", permission.Kind, proposals.Pending, p.ID, payload.RunID).Scan(&open); err != nil {
			return 0, false, err
		}
		return payload.RunID, open == 0, nil
	}
	return []proposals.Definition{{
		Kind: proposals.Kind{Name: permission.Kind, Module: unit.Assistant, Label: "Permission request", Icon: "lock", Approval: true},
		Execute: func(ctx context.Context, p *proposals.Proposal, _ any) (any, error) {
			run, ready, err := last(ctx, p)
			if err != nil {
				return nil, err
			}
			if ready {
				if err := resume(ctx, run); err != nil {
					return nil, err
				}
			}
			return map[string]any{"summary": "Permission request approved"}, nil
		},
		Dismiss: func(ctx context.Context, p *proposals.Proposal) error {
			run, ready, err := last(ctx, p)
			if err != nil || !ready {
				return err
			}
			err = resume(ctx, run)
			var disabled *module.OwnerDisabled
			if errors.As(err, &disabled) && disabled.Module == unit.Assistant {
				return nil
			}
			return err
		},
	}}
}

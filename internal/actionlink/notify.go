package actionlink

import (
	"context"
	"fmt"
	"time"

	"github.com/jeiang/atrium/internal/actionlink/policy"
	"github.com/jeiang/atrium/internal/assistant/permission"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/notify"
	"github.com/jeiang/atrium/internal/proposals"
)

func ProposalCreated() proposals.CreatedHook { return NotifyCreated }

func NotifyCreated(ctx context.Context, d *db.DB, p *proposals.Proposal) {
	pol, err := policy.Load(ctx, d.Read)
	if err != nil {
		return
	}
	allowed := pol.Allows(p.Kind)
	if !allowed && p.Kind != permission.Kind {
		return
	}
	now := float64(time.Now().UnixNano()) / 1e9
	title, body := Text(p.Kind, p.ID)
	msg := notify.Message{
		Channel: notify.Assistant, Title: title, Body: body, Click: fmt.Sprintf("/proposals/%d", p.ID),
		Now: func() float64 { return now }, Occurrence: fmt.Sprintf("proposal:%d", p.ID),
	}
	if allowed {
		approve, dismiss, err := MintPair(ctx, d.Write, p.ID, p.Fingerprint, now)
		if err != nil {
			return
		}
		msg.Actions = []notify.Action{
			{Action: "view", Label: "Approve", URL: "/actions/" + approve, Method: "POST"},
			{Action: "view", Label: "Dismiss", URL: "/actions/" + dismiss, Method: "POST"},
		}
	}
	_, _ = notify.Send(ctx, d, msg)
}

func Text(kind string, proposalID int64) (string, string) {
	return "atrium: " + kind + " proposal", fmt.Sprintf("Proposal #%d (%s). Approve or dismiss from this notification.", proposalID, kind)
}

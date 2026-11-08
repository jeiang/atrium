package actionlink

import (
	"context"
	"fmt"
	"time"

	"github.com/jeiang/atrium/internal/actionlink/policy"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/notify"
	"github.com/jeiang/atrium/internal/proposals"
)

func init() { proposals.OnCreate = NotifyCreated }

func NotifyCreated(ctx context.Context, d *db.DB, p *proposals.Proposal) {
	pol, err := policy.Load(ctx, d.Read)
	if err != nil || !pol.Allows(p.Kind) {
		return
	}
	now := float64(time.Now().UnixNano()) / 1e9
	approve, dismiss, err := MintPair(ctx, d.Write, p.ID, p.Fingerprint, now)
	if err != nil {
		return
	}
	title, body := Text(p.Kind, p.ID)
	_, _ = notify.Send(ctx, d.Write, config.Map(nil), notify.Message{
		Channel: notify.Assistant, Title: title, Body: body,
		Actions: []notify.Action{
			{Action: "view", Label: "Approve", URL: "/actions/" + approve, Method: "POST"},
			{Action: "view", Label: "Dismiss", URL: "/actions/" + dismiss, Method: "POST"},
		},
		Now: func() float64 { return now },
	})
}

func Text(kind string, proposalID int64) (string, string) {
	return "atrium: " + kind + " proposal", fmt.Sprintf("Proposal #%d (%s). Approve or dismiss from this notification.", proposalID, kind)
}

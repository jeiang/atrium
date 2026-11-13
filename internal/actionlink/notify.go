package actionlink

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jeiang/atrium/internal/actionlink/policy"
	"github.com/jeiang/atrium/internal/assistant/permission"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/notify"
	"github.com/jeiang/atrium/internal/proposals"
)

const refKind = "proposal"

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
		if err != nil && !errors.Is(err, ErrRate) {
			return
		}
		msg.Refs = proposalRefs(p.ID)
		if err == nil {
			msg.Actions = present(msg.Refs, p.ID, approve, dismiss)
		}
	}
	_, _ = notify.Send(ctx, d, msg)
}

func proposalRefs(id int64) []notify.ActionRef {
	return []notify.ActionRef{
		{ID: string(Approve), Kind: refKind, TargetID: id, Decision: string(Approve), Label: "Approve"},
		{ID: string(Dismiss), Kind: refKind, TargetID: id, Decision: string(Dismiss), Label: "Dismiss"},
	}
}

func present(refs []notify.ActionRef, id int64, approve, dismiss string) []notify.Action {
	var out []notify.Action
	for _, ref := range refs {
		if ref.Kind != refKind || ref.TargetID != id {
			continue
		}
		switch ref.Decision {
		case string(Approve):
			out = append(out, notify.Action{Action: "view", Label: ref.Label, URL: "/actions/" + approve, Method: "POST"})
		case string(Dismiss):
			out = append(out, notify.Action{Action: "view", Label: ref.Label, URL: "/actions/" + dismiss, Method: "POST"})
		}
	}
	return out
}

func Present(ctx context.Context, d *db.DB, refs []notify.ActionRef, now float64) ([]notify.Action, error) {
	pol, err := policy.Load(ctx, d.Read)
	if err != nil {
		return nil, err
	}
	var out []notify.Action
	seen := map[int64]bool{}
	for _, ref := range refs {
		if ref.Kind != refKind || seen[ref.TargetID] || (ref.ExpiresAt != nil && *ref.ExpiresAt <= now) {
			continue
		}
		seen[ref.TargetID] = true
		p, err := proposals.Get(ctx, d.Read, ref.TargetID)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && (p == nil || p.Status != proposals.Pending || !pol.Allows(p.Kind))) {
			continue
		}
		if err != nil {
			return nil, err
		}
		approve, dismiss, err := MintPair(ctx, d.Write, p.ID, p.Fingerprint, now)
		if errors.Is(err, ErrRate) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		out = append(out, present(refs, p.ID, approve, dismiss)...)
	}
	return out, nil
}

func Text(kind string, proposalID int64) (string, string) {
	return "atrium: " + kind + " proposal", fmt.Sprintf("Proposal #%d (%s). Approve or dismiss from this notification.", proposalID, kind)
}

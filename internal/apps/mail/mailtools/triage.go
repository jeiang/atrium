package mailtools

import (
	"context"
	"strings"

	mailapp "github.com/jeiang/atrium/internal/apps/mail/application"
	proposalsapp "github.com/jeiang/atrium/internal/proposals/application"
	"github.com/jeiang/atrium/internal/tool"
	"github.com/jeiang/atrium/internal/unit"
)

func Proposals(svc func(*tool.Context) *mailapp.Service) []proposalsapp.Definition {
	return []proposalsapp.Definition{{
		Kind: proposalsapp.Kind{Name: mailapp.MailTriage, Module: unit.Mail, Locks: []string{mailapp.PassResource}},
		Build: &proposalsapp.Builder{
			Description: "{items: [{id, action, label?}]}: propose archive or label for a batch of messages; one proposal per run",
			Build: func(ctx context.Context, tc *tool.Context, payload map[string]any) (*int64, error) {
				return proposeTriage(ctx, tc, payload, svc)
			},
		},
	}}
}

type triageRequest struct {
	ID     int64
	Action string
	Label  string
}

func triageRequests(payload map[string]any) ([]triageRequest, error) {
	raw, ok := payload["items"]
	if !ok {
		return nil, tool.Error("items is required")
	}
	list, ok := raw.([]any)
	if !ok || len(list) == 0 {
		return nil, tool.Error("items must be a non-empty array")
	}
	out := make([]triageRequest, 0, len(list))
	for _, entry := range list {
		item, ok := entry.(map[string]any)
		if !ok {
			return nil, tool.Error("each item must be an object")
		}
		id, err := intArg(item["id"], "each item needs a message id")
		if err != nil {
			return nil, err
		}
		action, ok := item["action"].(string)
		if !ok {
			return nil, tool.Error("each item needs an action")
		}
		switch action {
		case mailapp.TriageArchive, mailapp.TriageLabel:
		default:
			return nil, tool.Error("action must be archive or label")
		}
		req := triageRequest{ID: id, Action: action}
		if label, ok := item["label"].(string); ok {
			req.Label = strings.TrimSpace(label)
		}
		if req.Action == mailapp.TriageLabel && req.Label == "" {
			return nil, tool.Error("a label action needs a label")
		}
		out = append(out, req)
	}
	return out, nil
}

func proposeTriage(ctx context.Context, tc *tool.Context, payload map[string]any, svc func(*tool.Context) *mailapp.Service) (*int64, error) {
	requests, err := triageRequests(payload)
	if err != nil {
		return nil, err
	}
	items := make([]mailapp.TriageRequest, len(requests))
	for i, req := range requests {
		items[i] = mailapp.TriageRequest(req)
	}
	done, err := svc(tc).ProposeTriage(ctx, mailapp.ProposeTriageInput{Items: items})
	if err != nil {
		return nil, mailboxFail(err)
	}
	return &done.Data.ProposalID, nil
}

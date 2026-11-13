package mailtools

import (
	"context"
	"strings"

	"github.com/jeiang/atrium/internal/mail"
	mailapp "github.com/jeiang/atrium/internal/mail/application"
	"github.com/jeiang/atrium/internal/mail/executors"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/proposals"
	"github.com/jeiang/atrium/internal/tool"
)

func Proposals(svc func(*tool.Context) *mailapp.Service) []proposals.Definition {
	return []proposals.Definition{{
		Kind: proposals.Kind{Name: executors.MailTriage, Module: module.Mail, Locks: []string{mail.PassResource}},
		Build: &proposals.Builder{
			Description: "{items: [{id, action, label?, due?}]}: propose archive, label or snooze for a batch of messages; one proposal per run",
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
	Due    string
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
		case executors.TriageArchive, executors.TriageLabel, executors.TriageSnooze:
		default:
			return nil, tool.Error("action must be archive, label or snooze")
		}
		req := triageRequest{ID: id, Action: action}
		if label, ok := item["label"].(string); ok {
			req.Label = strings.TrimSpace(label)
		}
		if req.Action == executors.TriageLabel && req.Label == "" {
			return nil, tool.Error("a label action needs a label")
		}
		if due, ok := item["due"].(string); ok {
			req.Due = strings.TrimSpace(due)
		}
		if req.Action == executors.TriageSnooze && req.Due == "" {
			return nil, tool.Error("a snooze action needs a due time")
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

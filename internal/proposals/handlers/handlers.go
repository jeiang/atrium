package handlers

import (
	"context"
	"encoding/json"

	"github.com/jeiang/atrium/internal/assistant/approval"
	"github.com/jeiang/atrium/internal/assistant/permission"
	"github.com/jeiang/atrium/internal/assistant/toolset"
	"github.com/jeiang/atrium/internal/calendar"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/executors"
	"github.com/jeiang/atrium/internal/mail/openloops"
	mailsync "github.com/jeiang/atrium/internal/mail/sync"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/proposals"
)

type Deps struct {
	DB     *db.DB
	Open   mail.OpenFunc
	Syncer *mailsync.Syncer
	Now    func() float64
	Env    config.Env
	Resume func(ctx context.Context, runID int64) error
}

func Register(r *proposals.Registry, deps Deps) {
	executors.Register(r, &executors.Executors{DB: deps.DB, Open: deps.Open, Now: deps.Now}, deps.Syncer, module.Mail)
	calendar.Register(r, &calendar.Proposals{DB: deps.DB, Now: deps.Now, Env: deps.Env}, module.Calendar)
	openloops.Register(r, &openloops.Reminders{DB: deps.DB, Now: deps.Now}, module.Mail)
	(&approval.Executor{DB: deps.DB, Env: deps.Env, Now: deps.Now, Undoers: toolset.Undoers(deps.Env), Tools: toolset.Approvals()}).Register(r)
	if deps.Resume != nil {
		registerPermission(r, deps.Resume)
	}
}

func registerPermission(r *proposals.Registry, resume func(ctx context.Context, runID int64) error) {
	r.Executors[permission.Kind] = func(ctx context.Context, p *proposals.Proposal, _ any) (any, error) {
		var payload permission.Payload
		if err := json.Unmarshal(p.Payload, &payload); err != nil {
			return nil, err
		}
		if err := resume(ctx, payload.RunID); err != nil {
			return nil, err
		}
		return map[string]any{"summary": "Permission request approved"}, nil
	}
	r.AddKind(proposals.Kind{Name: permission.Kind, Module: module.Assistant, Label: "Permission request", Icon: "lock", Approval: true})
	r.Dismissers[permission.Kind] = func(ctx context.Context, p *proposals.Proposal) error {
		var payload permission.Payload
		if err := json.Unmarshal(p.Payload, &payload); err != nil {
			return err
		}
		return resume(ctx, payload.RunID)
	}
}

package application

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/mail/drafts"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/settings/audit"
)

const (
	DraftSendName    application.Name = "mail.draft-send"
	DraftResolveName application.Name = "mail.draft-resolve"
)

var log = logx.Logger("atrium.mail")

type DraftSendInput struct {
	ID       int64
	Revision int64
}
type DraftSendResult struct{ Sent drafts.Result }
type DraftResolveInput struct {
	ID      int64
	Outcome string
}
type DraftResolveResult struct{ Status drafts.Status }

func DescribeDraftSend() application.Definition {
	return application.Describe[DraftSendInput, DraftSendResult](DraftSendName, application.Command, []application.Authority{application.OperatorSend}, application.External, true)
}
func DescribeDraftResolve() application.Definition {
	return application.Describe[DraftResolveInput, DraftResolveResult](DraftResolveName, application.Command, []application.Authority{application.Operator}, application.NoEffect, true)
}

func (s *Service) WithSender(st *drafts.Store) *Service { s.sender = st; return s }

func operatorSource(ctx context.Context) bool {
	return audit.SourceOf(ctx) == audit.UI || audit.SourceOf(ctx) == audit.API
}

func (s *Service) writeAudit(ctx context.Context, name application.Name, target string, outcome state.Outcome, effects []int64) int64 {
	details := "{}"
	if len(effects) > 0 {
		raw, err := json.Marshal(map[string][]int64{"effect_ids": effects})
		if err == nil {
			details = string(raw)
		}
	}
	id, err := s.state.Audit(context.WithoutCancel(ctx), nil, state.Entry{Operation: string(name), Outcome: outcome, Target: target, DetailsJSON: details})
	if err != nil {
		log.Error("operation audit failed", "operation", string(name), logx.Err, err.Error())
	}
	return id
}

func auditOutcome(err error) state.Outcome {
	var classified *application.Error
	if !errors.As(err, &classified) {
		return state.Failed
	}
	switch classified.Code {
	case application.Invalid:
		return state.Invalid
	case application.Conflict:
		return state.Conflict
	case application.NotFound:
		return state.NotFound
	case application.Unknown:
		return state.Unknown
	case application.Forbidden, application.Refused, application.NotConfigured:
		return state.Refused
	}
	return state.Failed
}

func (s *Service) DraftSend(ctx context.Context, in DraftSendInput) (application.Change[DraftSendResult], error) {
	target := strconv.FormatInt(in.ID, 10)
	if !operatorSource(ctx) {
		s.writeAudit(ctx, DraftSendName, target, state.Refused, nil)
		return application.Change[DraftSendResult]{}, &application.Error{Code: application.Forbidden, Message: "only the operator can send a draft"}
	}
	result, err := s.sender.Send(ctx, in.ID, in.Revision)
	if err != nil {
		var unknown drafts.OutcomeUnknown
		var refused drafts.Error
		outcome := state.Failed
		switch {
		case errors.As(err, &unknown):
			outcome = state.Unknown
		case result.Filed == drafts.FiledFailed:
			outcome = state.Partial
		case result.Stale:
			outcome = state.Conflict
		case errors.As(err, &refused):
			outcome = state.Refused
		}
		id := s.writeAudit(ctx, DraftSendName, target, outcome, result.EffectIDs)
		return application.Change[DraftSendResult]{EffectIDs: result.EffectIDs, AuditID: id}, err
	}
	outcome := state.OK
	if result.Filed == drafts.FiledFailed {
		outcome = state.Partial
	}
	return application.Change[DraftSendResult]{Data: DraftSendResult{Sent: result}, EffectIDs: result.EffectIDs, AuditID: s.writeAudit(ctx, DraftSendName, target, outcome, result.EffectIDs)}, nil
}

func (s *Service) DraftResolve(ctx context.Context, in DraftResolveInput) (application.Change[DraftResolveResult], error) {
	target := strconv.FormatInt(in.ID, 10)
	var verdict drafts.Status
	switch in.Outcome {
	case "sent":
		verdict = drafts.Sent
	case "draft":
		verdict = drafts.Draft
	default:
		s.writeAudit(ctx, DraftResolveName, target, state.Invalid, nil)
		return application.Change[DraftResolveResult]{}, &application.Error{Code: application.Invalid, Message: "outcome must be sent or draft",
			Fields: []application.Field{{Name: "outcome", Code: "enum", Message: "Input should be 'sent' or 'draft'"}}}
	}
	if !operatorSource(ctx) {
		s.writeAudit(ctx, DraftResolveName, target, state.Refused, nil)
		return application.Change[DraftResolveResult]{}, &application.Error{Code: application.Forbidden, Message: "only the operator can resolve a draft"}
	}
	effect, err := s.sender.ResolveUnknown(ctx, in.ID, verdict, ledger.Resolver{Actor: "operator", Source: "ui"})
	var effects []int64
	if effect != 0 {
		effects = []int64{effect}
	}
	if err != nil {
		var refused drafts.Error
		var disabled *module.OwnerDisabled
		outcome := state.Failed
		if errors.As(err, &refused) || errors.As(err, &disabled) {
			outcome = state.Refused
		}
		id := s.writeAudit(ctx, DraftResolveName, target, outcome, effects)
		return application.Change[DraftResolveResult]{EffectIDs: effects, AuditID: id}, err
	}
	return application.Change[DraftResolveResult]{Data: DraftResolveResult{Status: verdict}, EffectIDs: effects, AuditID: s.writeAudit(ctx, DraftResolveName, target, state.OK, effects)}, nil
}

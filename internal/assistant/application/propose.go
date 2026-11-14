package application

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/proposals"
	"github.com/jeiang/atrium/internal/settings/audit"
	"github.com/jeiang/atrium/internal/tool"
)

const ProposeName application.Name = "review.propose"

type ProposeInput struct {
	Kind    string
	Present bool
	Payload any
}

type ProposeResult struct {
	ProposalID *int64 `json:"proposal_id"`
	Status     string `json:"status,omitempty"`
	Note       string `json:"note"`
}

func DescribePropose() application.Definition {
	return application.Describe[ProposeInput, ProposeResult](ProposeName, application.Command, []application.Authority{application.Agent}, application.Conditional, true)
}

func (s *Service) WithProposalBuilders(tc *tool.Context, builders map[string]*proposals.Builder, kinds []string) *Service {
	out := *s
	out.proposalContext, out.proposalBuilders, out.proposalKinds = tc, builders, kinds
	return &out
}

func (s *Service) Propose(ctx context.Context, in ProposeInput) (out application.Change[ProposeResult], err error) {
	outcome := state.Invalid
	auditCtx := audit.With(context.WithoutCancel(ctx), "assistant", audit.Tool)
	defer func() {
		target := ""
		if out.Data.ProposalID != nil {
			target = strconv.FormatInt(*out.Data.ProposalID, 10)
		}
		id, auditErr := s.audit(auditCtx, nil, ProposeName, target, outcome)
		out.AuditID = id
		err = errors.Join(err, auditErr)
	}()
	shown := "unknown"
	if in.Present {
		shown = strconv.Quote(in.Kind)
	}
	builder := s.proposalBuilders[in.Kind]
	if builder == nil || builder.Build == nil {
		return out, tool.Error("unknown proposal kind " + shown + "; allowed: " + strings.Join(s.proposalKinds, ", "))
	}
	dict, ok := in.Payload.(map[string]any)
	if !ok {
		return out, tool.Error("payload must be an object")
	}
	outcome = state.Failed
	id, err := builder.Build(ctx, s.proposalContext, dict)
	if err != nil {
		var failure *application.Error
		if errors.As(err, &failure) {
			if classified, ok := outcomeOf(failure); ok {
				outcome = classified
			}
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			outcome = state.Canceled
		}
		return out, err
	}
	outcome = state.OK
	if id == nil {
		outcome = state.Noop
		out.Data = ProposeResult{Note: "an identical proposal already exists"}
	} else {
		out.Data = ProposeResult{ProposalID: id, Status: "pending", Note: "the operator approves or dismisses the card"}
	}
	return out, nil
}

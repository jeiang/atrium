package application

import (
	"context"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/proposals"
)

var log = logx.Logger("atrium.tasks")

const ProposeReminderName application.Name = "tasks.propose-reminder"

type ProposeReminderInput struct {
	Kind, Title, Fingerprint string
	Payload, Preview         map[string]any
	Now                      float64
}

type ProposedReminder struct {
	ProposalID int64
	Created    bool
}

func DescribeProposeReminder() application.Definition {
	return application.Describe[ProposeReminderInput, ProposedReminder](ProposeReminderName, application.Command, []application.Authority{application.System}, application.NoEffect, true)
}

func (s *Service) ProposeReminder(ctx context.Context, in ProposeReminderInput) (application.Change[ProposedReminder], error) {
	pid, created, err := proposals.Create(ctx, s.db, in.Kind, nil, in.Title, in.Payload, in.Preview, in.Fingerprint, in.Now)
	if err != nil {
		return application.Change[ProposedReminder]{}, err
	}
	out := application.Change[ProposedReminder]{Data: ProposedReminder{ProposalID: pid, Created: created}}
	if !created {
		if out.Data.ProposalID, err = proposals.IDByFingerprint(ctx, s.db.Write, in.Fingerprint); err != nil {
			return application.Change[ProposedReminder]{}, err
		}
		return out, nil
	}
	if out.AuditID, err = s.audit(context.WithoutCancel(ctx), nil, ProposeReminderName, pid, state.OK, 0); err != nil {
		log.Error("operation audit failed", "operation", string(ProposeReminderName), logx.Err, err.Error())
	}
	return out, nil
}

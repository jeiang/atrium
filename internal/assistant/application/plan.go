package application

import (
	"context"
	"database/sql"
	"errors"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/assistant/progress"
	"github.com/jeiang/atrium/internal/assistant/workspace"
)

const PlanReplaceName application.Name = "assistant.plan.replace"

var agentWriters = []application.Authority{application.Agent}

type PlanReplaceInput struct {
	ThreadID int64
	RunID    int64
	Items    any
}

type PlanStep struct {
	Text string `json:"text"`
	Done bool   `json:"done"`
}

type PlanReplaced struct {
	Items []PlanStep `json:"items"`
	Done  int        `json:"done"`
	Total int        `json:"total"`
}

func DescribePlanReplace() application.Definition {
	return application.Describe[PlanReplaceInput, PlanReplaced](PlanReplaceName, application.Command, agentWriters, application.NoEffect, true)
}

func (s *Service) PlanReplace(ctx context.Context, in PlanReplaceInput) (application.Change[PlanReplaced], error) {
	now := s.now()
	where := target("thread", in.ThreadID)
	var out PlanReplaced
	var auditID int64
	err := s.db.WithTx(ctx, func(tx *sql.Tx) error {
		items, err := workspace.SetPlan(ctx, tx, in.ThreadID, &in.RunID, in.Items, now)
		if err != nil {
			return err
		}
		out = PlanReplaced{Items: make([]PlanStep, len(items)), Total: len(items)}
		steps := make([]any, len(items))
		for i, it := range items {
			out.Items[i] = PlanStep{Text: it.Text, Done: it.Done}
			steps[i] = map[string]any{"text": it.Text, "done": it.Done}
			if it.Done {
				out.Done++
			}
		}
		if err := progress.Emit(ctx, tx, in.RunID, progress.Plan, map[string]any{"items": steps}, now); err != nil {
			return err
		}
		auditID, err = s.audit(ctx, tx, PlanReplaceName, where, state.OK)
		return err
	})
	var pe workspace.PlanError
	if errors.As(err, &pe) {
		err = &application.Error{Code: application.Invalid, Message: pe.Error(), Cause: err}
	}
	if err != nil {
		return application.Change[PlanReplaced]{}, s.reject(ctx, PlanReplaceName, where, err)
	}
	return application.Change[PlanReplaced]{Data: out, AuditID: auditID}, nil
}

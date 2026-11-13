package application

import (
	"context"
	"slices"
	"time"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/contacts"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/mail/triage"
)

const TriageName application.Name = "mail.triage"

type TriageInput struct {
	Account, Stage, Agreement, Q string
	Offset                       int
	Full                         bool
}
type TriageResult struct {
	Summaries   []triage.Summary  `json:"summaries"`
	Flags       []triage.Judgment `json:"flags"`
	FlagsCapped bool              `json:"flags_capped"`
	NoContacts  bool              `json:"no_contacts"`
	Rows        []triage.Row      `json:"rows"`
	Check       []triage.Row      `json:"check"`
	Total       int               `json:"total"`
	Offset      int               `json:"offset"`
	NextOffset  int               `json:"next_offset"`
	Account     string            `json:"account"`
	Stage       string            `json:"stage"`
	Agreement   string            `json:"agreement"`
	Q           string            `json:"q"`
}

func DescribeTriage() application.Definition {
	return application.Describe[TriageInput, TriageResult](TriageName, application.Query, []application.Authority{application.Operator}, application.NoEffect, false)
}
func (s *Service) Triage(ctx context.Context, in TriageInput) (application.Read[TriageResult], error) {
	const pageSize = 50
	const checkRows = 20
	data := TriageResult{Account: in.Account, Stage: in.Stage, Agreement: in.Agreement, Q: in.Q, Offset: in.Offset}
	rev, err := s.snapshot(ctx, TriageName, in, func(q db.Querier) error {
		filter := triage.Filter{Account: in.Account, Stage: in.Stage, Agreement: in.Agreement, Limit: pageSize, Offset: in.Offset, Q: in.Q}
		plain := in.Stage == "" && in.Agreement == "" && in.Q == ""
		if plain {
			filter.Attention = triage.AttentionRest
		}
		found, err := triage.Triage(ctx, q, filter)
		if err != nil {
			return err
		}
		data.Rows, data.Total = found.Rows, found.Total
		if shown := in.Offset + len(found.Rows); shown < found.Total {
			data.NextOffset = shown
		}
		if plain && in.Offset == 0 {
			found, err := triage.Triage(ctx, q, triage.Filter{Account: in.Account, Attention: triage.AttentionCheck, PerSender: true, Limit: checkRows})
			if err != nil {
				return err
			}
			data.Check = found.Rows
		}
		if !in.Full {
			return nil
		}
		summaries, err := triage.Summaries(ctx, q, s.env, nil, time.Now())
		if err != nil {
			return err
		}
		data.Summaries = slices.DeleteFunc(summaries, func(v triage.Summary) bool { return in.Account != "" && v.Account != in.Account })
		flags, err := triage.FlagJudgments(ctx, q, in.Account, pageSize+1, 0)
		if err != nil {
			return err
		}
		data.FlagsCapped = len(flags) > pageSize
		data.Flags = flags[:min(len(flags), pageSize)]
		dir, err := contacts.Dir(ctx, q)
		data.NoContacts = dir == ""
		return err
	})
	return application.Read[TriageResult]{Revision: rev, Data: data}, err
}

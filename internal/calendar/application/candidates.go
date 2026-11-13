package application

import (
	"context"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/calendar"
	"github.com/jeiang/atrium/internal/calendar/candidates"
	"github.com/jeiang/atrium/internal/davmirror"
)

const DraftCandidatesName application.Name = "calendar.draft-candidates"

type DraftCandidatesInput struct{}

func (s *Service) WithDraftRun(run candidates.Run) *Service {
	out := *s
	out.draftRun = run
	return &out
}

func (s *Service) DraftCandidates(ctx context.Context, _ DraftCandidatesInput) (application.Change[candidates.Result], error) {
	_, limit, err := calendar.CandidateLimits(ctx, s.db.Read)
	var result candidates.Result
	if err == nil {
		result, err = candidates.DraftEventProposals(ctx, s.db, s.draftRun, limit, s.now)
	}
	outcome := state.OK
	if err != nil {
		outcome = outcomeOf(err)
	} else if result.Candidates == 0 {
		outcome = state.Noop
	}
	id := s.settled(ctx, DraftCandidatesName, outcome, "", map[string]any{})
	return application.Change[candidates.Result]{Data: result, AuditID: id}, err
}

func DescribePasses() []application.Definition {
	authorities := []application.Authority{application.Operator, application.System}
	return []application.Definition{
		application.Describe[SyncInput, davmirror.Stats](SyncName, application.Command, authorities, application.External, true),
		application.Describe[DraftCandidatesInput, candidates.Result](DraftCandidatesName, application.Command, authorities, application.External, true),
	}
}

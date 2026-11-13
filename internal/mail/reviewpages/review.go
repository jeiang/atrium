package reviewpages

import (
	"context"
	"net/http"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/mail/drafts"
	"github.com/jeiang/atrium/internal/proposals"
	reviewapp "github.com/jeiang/atrium/internal/review/application"
)

func needsCount(ctx context.Context, q db.Querier) (int, error) {
	n, err := proposals.NeedsCount(ctx, q)
	if err != nil {
		return 0, err
	}
	d, err := drafts.CountNeeds(ctx, q)
	return n + d, err
}

type needsData struct {
	Proposals  []ProposalView      `json:"proposals"`
	Blocked    []ProposalView      `json:"blocked"`
	Drafts     []DraftView         `json:"drafts"`
	Findings   []reviewapp.Finding `json:"findings"`
	Configured map[string]bool     `json:"configured"`
}

func (s *service) needsPage(w http.ResponseWriter, r *http.Request) {
	found, err := s.review.Pending(r.Context(), reviewapp.PendingInput{Findings: true})
	if err != nil {
		s.failRead(w, r, err, "")
		return
	}
	data := needsData{
		Proposals: proposalViews(found.Data.Approvals), Blocked: proposalViews(found.Data.Blocked),
		Drafts: draftViews(found.Data.Drafts), Findings: found.Data.Findings, Configured: found.Data.Configured,
	}
	s.page(w, r, http.StatusOK, "review_needs", "review.gohtml", data, nil)
}

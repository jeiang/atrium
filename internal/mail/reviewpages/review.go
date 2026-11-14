package reviewpages

import (
	"net/http"

	reviewapp "github.com/jeiang/atrium/internal/review/application"
)

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

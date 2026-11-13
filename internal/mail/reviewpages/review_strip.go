package reviewpages

import (
	"net/http"

	reviewapp "github.com/jeiang/atrium/internal/review/application"
	"github.com/jeiang/atrium/internal/web"
)

const (
	stripItems  = 3
	recentItems = 6
)

type stripData struct {
	Proposals  []ProposalView  `json:"proposals"`
	Blocked    []ProposalView  `json:"blocked"`
	Drafts     []DraftView     `json:"drafts"`
	Configured map[string]bool `json:"configured"`
	Count      int             `json:"count"`
	More       bool            `json:"more"`
}

type receiptsData struct {
	Entries []reviewapp.ActivityEntry `json:"entries"`
}

func (s *service) needsStrip(w http.ResponseWriter, r *http.Request) {
	found, err := s.review.Pending(r.Context(), reviewapp.PendingInput{Limit: stripItems})
	if err != nil {
		s.failRead(w, r, err, "")
		return
	}
	left := stripItems
	take := func(n int) int {
		n = min(n, left)
		left -= n
		return n
	}
	pending := found.Data.Approvals[:take(len(found.Data.Approvals))]
	blocked := found.Data.Blocked[:take(len(found.Data.Blocked))]
	rows := found.Data.Drafts[:take(len(found.Data.Drafts))]
	s.fragment(w, r, http.StatusOK, "_review_strip.gohtml", stripData{
		Proposals: proposalViews(pending), Blocked: proposalViews(blocked),
		Drafts: draftViews(rows), Configured: found.Data.Configured,
		Count: found.Data.Count, More: found.Data.Count > stripItems-left,
	}, nil)
}

func (s *service) recentActivity(w http.ResponseWriter, r *http.Request) {
	found, err := s.review.Activity(r.Context(), reviewapp.ActivityInput{})
	if err != nil {
		s.failRead(w, r, err, "")
		return
	}
	kept := make([]reviewapp.ActivityEntry, 0, recentItems)
	for _, e := range found.Data.Entries {
		if len(kept) == recentItems {
			break
		}
		if e.Write != nil || e.Mail != nil {
			kept = append(kept, e)
		}
	}
	s.fragment(w, r, http.StatusOK, "_receipts.gohtml", receiptsData{Entries: kept}, nil)
}

func receiptView(r *http.Request) bool {
	return web.FormString(web.FormValues(r), "view", "") == "receipt"
}

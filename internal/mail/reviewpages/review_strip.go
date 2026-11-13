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
	s.fragment(w, r, http.StatusOK, "_review_strip.gohtml", StripView(found.Data), nil)
}

func StripView(found reviewapp.Pending) stripData {
	left := stripItems
	take := func(n int) int {
		n = min(n, left)
		left -= n
		return n
	}
	pending := found.Approvals[:take(len(found.Approvals))]
	blocked := found.Blocked[:take(len(found.Blocked))]
	rows := found.Drafts[:take(len(found.Drafts))]
	return stripData{
		Proposals: proposalViews(pending), Blocked: proposalViews(blocked),
		Drafts: draftViews(rows), Configured: found.Configured,
		Count: found.Count, More: found.Count > stripItems-left,
	}
}

func (s *service) recentActivity(w http.ResponseWriter, r *http.Request) {
	found, err := s.review.Activity(r.Context(), reviewapp.ActivityInput{})
	if err != nil {
		s.failRead(w, r, err, "")
		return
	}
	s.fragment(w, r, http.StatusOK, "_receipts.gohtml", ReceiptsView(found.Data), nil)
}

func ReceiptsView(found reviewapp.Activity) receiptsData {
	kept := make([]reviewapp.ActivityEntry, 0, recentItems)
	for _, e := range found.Entries {
		if len(kept) == recentItems {
			break
		}
		if e.Write != nil || e.Mail != nil {
			kept = append(kept, e)
		}
	}
	return receiptsData{Entries: kept}
}

func receiptView(r *http.Request) bool {
	return web.FormString(web.FormValues(r), "view", "") == "receipt"
}

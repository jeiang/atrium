package reviewpages

import (
	"net/http"

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
	Entries []ActivityEntry `json:"entries"`
}

func (s *service) needsStrip(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	found := s.needs(ctx)
	left := stripItems
	take := func(n int) int {
		n = min(n, left)
		left -= n
		return n
	}
	pending := found.pending[:take(len(found.pending))]
	blocked := found.blocked[:take(len(found.blocked))]
	rows := found.drafts[:take(len(found.drafts))]
	count, err := needsCount(ctx, s.deps.DB.Read)
	check(err)
	s.fragment(w, r, http.StatusOK, "_review_strip.gohtml", stripData{
		Proposals: proposalViews(pending, s.registry), Blocked: proposalViews(blocked, s.registry),
		Drafts: s.draftViews(r, rows), Configured: accountConfigured(ctx),
		Count: count, More: count > stripItems-left,
	}, nil)
}

func (s *service) recentActivity(w http.ResponseWriter, r *http.Request) {
	entries, _, err := timeline(r.Context(), s.deps.DB.Read, "", pageSize, 0, s.now())
	check(err)
	kept := make([]ActivityEntry, 0, recentItems)
	for _, e := range entries {
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

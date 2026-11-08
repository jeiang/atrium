package reviewpages

import (
	"cmp"
	"context"
	"net/http"
	"slices"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/mail/drafts"
	"github.com/jeiang/atrium/internal/proposals"
)

const needsQuery = "SELECT (SELECT COUNT(*) FROM proposals WHERE status = 'pending') + " +
	"(SELECT COUNT(*) FROM drafts WHERE status IN ('draft', 'unknown'))"

func needsCount(ctx context.Context, q db.Querier) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, needsQuery).Scan(&n)
	return n, err
}

type needsData struct {
	Proposals  []ProposalView  `json:"proposals"`
	Drafts     []DraftView     `json:"drafts"`
	Configured map[string]bool `json:"configured"`
}

func (s *service) needsPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	pending, err := proposals.List(ctx, s.deps.DB.Read, proposals.Pending, "")
	check(err)
	open, err := s.drafts.List(ctx, drafts.Draft, draftLimit)
	check(err)
	unknown, err := s.drafts.List(ctx, drafts.Unknown, draftLimit)
	check(err)
	rows := append(unknown, open...)
	slices.SortStableFunc(rows, func(a, b drafts.Row) int { return cmp.Compare(b.UpdatedAt, a.UpdatedAt) })
	data := needsData{Proposals: proposalViews(pending, s.registry), Drafts: s.draftViews(r, rows), Configured: accountConfigured(ctx)}
	s.page(w, r, http.StatusOK, "review_needs", "review.gohtml", data, nil)
}

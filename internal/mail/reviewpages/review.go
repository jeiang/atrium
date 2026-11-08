package reviewpages

import (
	"cmp"
	"context"
	"net/http"
	"slices"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/mail/drafts"
	"github.com/jeiang/atrium/internal/proposals"
)

var needsQuery = "SELECT (SELECT COUNT(*) FROM proposals WHERE status = 'pending') + " +
	"(SELECT COUNT(*) FROM proposals p WHERE p.status <> 'pending' AND EXISTS " +
	"(SELECT 1 FROM proposal_actions a WHERE a.proposal_id = p.id AND " + ledger.BlockedSQL("a") + ")) + " +
	"(SELECT COUNT(*) FROM drafts WHERE status IN ('draft', 'unknown'))"

func needsCount(ctx context.Context, q db.Querier) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, needsQuery).Scan(&n)
	return n, err
}

type needsData struct {
	Proposals  []ProposalView  `json:"proposals"`
	Blocked    []ProposalView  `json:"blocked"`
	Drafts     []DraftView     `json:"drafts"`
	Configured map[string]bool `json:"configured"`
}

type needsLists struct {
	pending []*proposals.Proposal
	blocked []*proposals.Proposal
	drafts  []drafts.Row
}

func (s *service) needs(ctx context.Context) needsLists {
	pending, err := proposals.List(ctx, s.deps.DB.Read, proposals.Pending, "")
	check(err)
	blocked, err := proposals.ListBlocked(ctx, s.deps.DB.Read)
	check(err)
	open, err := s.drafts.List(ctx, drafts.Draft, draftLimit)
	check(err)
	unknown, err := s.drafts.List(ctx, drafts.Unknown, draftLimit)
	check(err)
	rows := append(unknown, open...)
	slices.SortStableFunc(rows, func(a, b drafts.Row) int { return cmp.Compare(b.UpdatedAt, a.UpdatedAt) })
	return needsLists{pending: pending, blocked: blocked, drafts: rows}
}

func (s *service) needsPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	found := s.needs(ctx)
	data := needsData{
		Proposals: proposalViews(found.pending, s.registry), Blocked: proposalViews(found.blocked, s.registry),
		Drafts: s.draftViews(r, found.drafts), Configured: accountConfigured(ctx),
	}
	s.page(w, r, http.StatusOK, "review_needs", "review.gohtml", data, nil)
}

package reviewpages

import (
	"cmp"
	"context"
	"net/http"
	"slices"
	"strings"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/mail/drafts"
	"github.com/jeiang/atrium/internal/proposals"
	"github.com/jeiang/atrium/internal/serverops"
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
	Proposals  []ProposalView  `json:"proposals"`
	Blocked    []ProposalView  `json:"blocked"`
	Drafts     []DraftView     `json:"drafts"`
	Findings   []FindingView   `json:"findings"`
	Configured map[string]bool `json:"configured"`
}

type FindingView struct {
	ID       int64   `json:"id"`
	Severity string  `json:"severity"`
	Host     string  `json:"host"`
	Metric   string  `json:"metric"`
	Title    string  `json:"title"`
	Body     string  `json:"body"`
	Created  float64 `json:"created"`
}

func (s *service) findings(ctx context.Context) []FindingView {
	found, err := serverops.ReviewFindings(ctx, s.deps.DB.Read, 20)
	check(err)
	out := make([]FindingView, 0, len(found))
	for _, f := range found {
		view := FindingView{ID: f.ID, Severity: f.Severity, Title: f.Title, Body: f.Body, Created: f.CreatedAt}
		view.Host, view.Metric, _ = strings.Cut(f.Fact, ":")
		out = append(out, view)
	}
	return out
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
		Drafts: s.draftViews(r, found.drafts), Findings: s.findings(ctx), Configured: accountConfigured(ctx),
	}
	s.page(w, r, http.StatusOK, "review_needs", "review.gohtml", data, nil)
}

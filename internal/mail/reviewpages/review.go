package reviewpages

import (
	"cmp"
	"context"
	"database/sql"
	"net/http"
	"slices"
	"strings"

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
	rows, err := s.deps.DB.Read.QueryContext(ctx,
		"SELECT id, fact, severity, title, body, created_at FROM ops_findings WHERE resolved_at IS NULL "+
			"ORDER BY created_at DESC, id DESC LIMIT 20")
	check(err)
	out, err := db.Collect(rows, func(r *sql.Rows) (FindingView, error) {
		var f FindingView
		var fact string
		if err := r.Scan(&f.ID, &fact, &f.Severity, &f.Title, &f.Body, &f.Created); err != nil {
			return FindingView{}, err
		}
		f.Host, f.Metric, _ = strings.Cut(fact, ":")
		return f, nil
	})
	check(err)
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

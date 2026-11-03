package sources

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/jeiang/atrium/internal/attention"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/proposals"
)

func Core() []attention.Source { return []attention.Source{attention.SourceFunc(pendingProposals)} }

func pendingProposals(ctx context.Context, q db.Querier, _ time.Time, _ *time.Location) ([]attention.Item, error) {
	list, err := proposals.List(ctx, q, proposals.Pending, "")
	if err != nil {
		return nil, err
	}
	out := make([]attention.Item, 0, len(list))
	for _, p := range list {
		id := strconv.FormatInt(p.ID, 10)
		out = append(out, attention.Item{
			Kind: attention.KindProposal, SourceID: id, Title: p.Title, Class: attention.NeedsDecision, TS: p.CreatedTS,
			Actions: []attention.Action{
				{Label: "Approve", Method: "POST", Path: fmt.Sprintf("/proposals/%s/approve", id)},
				{Label: "Dismiss", Method: "POST", Path: fmt.Sprintf("/proposals/%s/dismiss", id)},
			},
		})
	}
	return out, nil
}

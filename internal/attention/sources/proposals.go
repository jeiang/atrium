package sources

import (
	"context"
	"strconv"

	"github.com/jeiang/atrium/internal/attention"
	"github.com/jeiang/atrium/internal/db"
	proposalq "github.com/jeiang/atrium/internal/proposals"
)

type proposals struct{}

func Proposals() attention.Source { return proposals{} }

func (proposals) Name() string { return "proposals" }

func (proposals) Items(ctx context.Context, q db.Querier, _ float64) ([]attention.Item, error) {
	found, err := proposalq.PendingProposals(ctx, q)
	if err != nil {
		return nil, err
	}
	items := make([]attention.Item, 0, len(found))
	for _, p := range found {
		items = append(items, attention.Item{
			Kind: "proposal", SourceID: strconv.FormatInt(p.ID, 10), Title: p.Title, Detail: p.Kind,
			Created: p.CreatedTS, Group: attention.NeedsDecision,
			Actions: []attention.Action{{Kind: "approve", Label: "Approve"}, {Kind: "dismiss", Label: "Dismiss"}},
		})
	}
	return items, nil
}

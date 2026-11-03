package sources

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/jeiang/atrium/internal/attention"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/proposals"
	"github.com/jeiang/atrium/internal/proposals/executors"
)

var needsSelection = []string{executors.RebalanceSplit, executors.RebalanceMerge, executors.InboxBacklog, executors.DeletionRule, executors.GmailLabelMerge}

func Core() []attention.Source { return []attention.Source{attention.SourceFunc(pendingProposals)} }

func pendingProposals(ctx context.Context, q db.Querier, _ time.Time, _ *time.Location) ([]attention.Item, error) {
	list, err := proposals.List(ctx, q, proposals.Pending, "")
	if err != nil {
		return nil, err
	}
	out := make([]attention.Item, 0, len(list))
	for _, p := range list {
		id := strconv.FormatInt(p.ID, 10)
		actions := []attention.Action{{Label: "Dismiss", Method: "POST", Path: fmt.Sprintf("/proposals/%s/dismiss", id)}}
		if !slices.Contains(needsSelection, p.Kind) {
			actions = slices.Insert(actions, 0, attention.Action{Label: "Approve", Method: "POST", Path: fmt.Sprintf("/proposals/%s/approve", id)})
		}
		out = append(out, attention.Item{
			Kind: attention.KindProposal, SourceID: id, Title: p.Title, Class: attention.NeedsDecision, TS: p.CreatedTS,
			Actions: actions,
		})
	}
	return out, nil
}

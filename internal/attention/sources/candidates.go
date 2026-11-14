package sources

import (
	"context"
	"strconv"

	"github.com/jeiang/atrium/internal/attention"
	candidateq "github.com/jeiang/atrium/internal/calendar/candidates"
	"github.com/jeiang/atrium/internal/db"
)

type candidates struct{}

func Candidates() attention.Source { return candidates{} }

func (candidates) Name() string { return "candidates" }

func (candidates) Items(ctx context.Context, q db.Querier, _ float64) ([]attention.Item, error) {
	found, err := candidateq.PendingItems(ctx, q)
	if err != nil {
		return nil, err
	}
	items := make([]attention.Item, 0, len(found))
	for _, item := range found {
		items = append(items, attention.Item{
			Kind: "event_candidate", SourceID: strconv.FormatInt(item.MessageID, 10), Title: "Event candidate", Detail: item.Reason,
			Created: item.TS, Group: attention.NeedsDecision,
			Actions: []attention.Action{{Kind: "review", Label: "Review"}},
		})
	}
	return items, nil
}

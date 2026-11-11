package sources

import (
	"context"
	"strconv"

	"github.com/jeiang/atrium/internal/assistant/workspace"
	"github.com/jeiang/atrium/internal/attention"
	"github.com/jeiang/atrium/internal/db"
)

type plans struct{}

func Plans() attention.Source { return plans{} }

func (plans) Name() string { return "plans" }

func (plans) Items(ctx context.Context, q db.Querier, _ float64) ([]attention.Item, error) {
	found, err := workspace.Plans(ctx, q)
	if err != nil {
		return nil, err
	}
	items := make([]attention.Item, 0, len(found))
	for _, p := range found {
		items = append(items, attention.Item{
			Kind: "plan", SourceID: strconv.FormatInt(p.ThreadID, 10), Title: p.Title, Detail: "Thread plan",
			Created: p.UpdatedAt, Group: attention.NeedsDecision,
			Actions: []attention.Action{{Kind: "review", Label: "Review"}},
		})
	}
	return items, nil
}

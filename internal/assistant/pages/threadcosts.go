package pages

import (
	"context"

	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/inference"
)

func fillThreadCosts(ctx context.Context, q db.Querier, list []threads.Listed) error {
	ids := make([]int64, len(list))
	for i := range list {
		ids[i] = list[i].ID
	}
	costs, err := inference.ThreadCosts(ctx, q, ids)
	if err != nil {
		return err
	}
	for i := range list {
		list[i].Cost = costs[list[i].ID]
	}
	return nil
}

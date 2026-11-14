package service

import (
	"context"

	"github.com/jeiang/atrium/internal/assistant/journal"
	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/inference"
)

type scopes struct{}

func (scopes) RunAncestors(ctx context.Context, q db.Querier, runID int64) ([]inference.RunScope, error) {
	rows, err := journal.RunAncestors(ctx, q, runID)
	if err != nil {
		return nil, err
	}
	out := make([]inference.RunScope, len(rows))
	for i, r := range rows {
		out[i] = inference.RunScope{ID: r.ID, Model: r.Model, MaxCostUSD: r.MaxCostUSD}
	}
	return out, nil
}

func (scopes) RunDescendants(ctx context.Context, q db.Querier, runID int64) ([]int64, error) {
	return journal.RunDescendants(ctx, q, runID)
}

func (scopes) RunThread(ctx context.Context, q db.Querier, runID int64) (int64, error) {
	return journal.RunThread(ctx, q, runID)
}

func (scopes) ThreadTree(ctx context.Context, q db.Querier, threadID int64) (inference.ThreadScope, error) {
	tree, err := threads.ThreadTree(ctx, q, threadID)
	if err != nil {
		return inference.ThreadScope{}, err
	}
	return inference.ThreadScope{RootID: tree.RootID, CapUSD: tree.CapUSD, IDs: tree.IDs, AccountingOnly: tree.Kind == threads.Classification}, nil
}

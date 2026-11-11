package journal

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/jeiang/atrium/internal/db"
)

type Scope struct {
	ID         int64
	ThreadID   int64
	Model      string
	MaxCostUSD *float64
}

func RunAncestors(ctx context.Context, q db.Querier, runID int64) ([]Scope, error) {
	seen := map[int64]bool{}
	var out []Scope
	id := runID
	for id != 0 {
		if seen[id] {
			return nil, fmt.Errorf("journal: run %d has a cyclic parent chain", runID)
		}
		seen[id] = true
		run, err := GetRun(ctx, q, id)
		if err != nil {
			return nil, err
		}
		if run == nil {
			return nil, fmt.Errorf("journal: run %d: no run %d", runID, id)
		}
		limits, err := LimitsOf(ctx, q, id)
		if err != nil {
			return nil, err
		}
		out = append(out, Scope{ID: run.ID, ThreadID: run.ThreadID, Model: run.Model, MaxCostUSD: limits.MaxCostUSD})
		if run.ParentRunID == nil {
			break
		}
		id = *run.ParentRunID
	}
	return out, nil
}

func RunDescendants(ctx context.Context, q db.Querier, runID int64) ([]int64, error) {
	rows, err := q.QueryContext(ctx,
		"WITH RECURSIVE tree(id) AS (SELECT ? UNION SELECT r.id FROM runs r JOIN tree ON r.parent_run_id = tree.id) SELECT id FROM tree", runID)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (int64, error) {
		var id int64
		return id, r.Scan(&id)
	})
}

func RunThread(ctx context.Context, q db.Querier, runID int64) (int64, error) {
	var thread int64
	err := q.QueryRowContext(ctx, "SELECT thread_id FROM runs WHERE id = ?", runID).Scan(&thread)
	if err != nil {
		return 0, fmt.Errorf("journal: run %d: %w", runID, err)
	}
	return thread, nil
}

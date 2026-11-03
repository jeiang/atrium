package sources

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/jeiang/atrium/internal/assistant/workspace"
	"github.com/jeiang/atrium/internal/attention"
	"github.com/jeiang/atrium/internal/db"
)

func Assistant() []attention.Source { return []attention.Source{attention.SourceFunc(openPlans)} }

func openPlans(ctx context.Context, q db.Querier, _ time.Time, _ *time.Location) ([]attention.Item, error) {
	rows, err := q.QueryContext(ctx,
		"SELECT p.thread_id, COALESCE(t.title, ''), p.items_json, p.updated_at FROM thread_plans p JOIN threads t ON t.id = p.thread_id "+
			"WHERE t.archived = 0 ORDER BY p.updated_at, p.thread_id LIMIT ?", attention.QueueLimit)
	if err != nil {
		return nil, err
	}
	all, err := db.Collect(rows, func(r *sql.Rows) (attention.Item, error) {
		var id int64
		var title, raw string
		it := attention.Item{Kind: attention.KindThreadPlan, Class: attention.Info}
		if err := r.Scan(&id, &title, &raw, &it.TS); err != nil {
			return it, err
		}
		var steps []workspace.PlanItem
		if err := json.Unmarshal([]byte(raw), &steps); err != nil {
			return it, fmt.Errorf("thread %d plan: %w", id, err)
		}
		it.SourceID = strconv.FormatInt(id, 10)
		for _, s := range steps {
			if !s.Done {
				it.Title = s.Text
				if title != "" {
					it.Title = title + ": " + s.Text
				}
				break
			}
		}
		return it, nil
	})
	out := all[:0]
	for _, it := range all {
		if it.Title != "" {
			out = append(out, it)
		}
	}
	return out, err
}

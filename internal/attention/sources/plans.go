package sources

import (
	"context"
	"strconv"

	"github.com/jeiang/atrium/internal/attention"
	"github.com/jeiang/atrium/internal/db"
)

type plans struct{}

func Plans() attention.Source { return plans{} }

func (plans) Name() string { return "plans" }

func (plans) Items(ctx context.Context, q db.Querier, _ float64) ([]attention.Item, error) {
	rows, err := q.QueryContext(ctx,
		"SELECT p.thread_id, COALESCE(t.title, ''), p.updated_at FROM thread_plans p LEFT JOIN threads t ON t.id = p.thread_id ORDER BY p.updated_at")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []attention.Item
	for rows.Next() {
		var id int64
		var title string
		var updated float64
		if err := rows.Scan(&id, &title, &updated); err != nil {
			return nil, err
		}
		items = append(items, attention.Item{
			Kind: "plan", SourceID: strconv.FormatInt(id, 10), Title: title, Detail: "Thread plan",
			Created: updated, Group: attention.NeedsDecision,
			Actions: []attention.Action{{Kind: "review", Label: "Review"}},
		})
	}
	return items, rows.Err()
}

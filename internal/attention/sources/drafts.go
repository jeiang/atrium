package sources

import (
	"context"
	"strconv"

	"github.com/jeiang/atrium/internal/attention"
	"github.com/jeiang/atrium/internal/db"
)

type drafts struct{}

func Drafts() attention.Source { return drafts{} }

func (drafts) Name() string { return "drafts" }

func (drafts) Items(ctx context.Context, q db.Querier, _ float64) ([]attention.Item, error) {
	rows, err := q.QueryContext(ctx, "SELECT id, subject, to_addrs, updated_at FROM drafts WHERE status = 'draft' ORDER BY updated_at")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var items []attention.Item
	for rows.Next() {
		var id int64
		var subject, to string
		var updated float64
		if err := rows.Scan(&id, &subject, &to, &updated); err != nil {
			return nil, err
		}
		items = append(items, attention.Item{
			Kind: "draft", SourceID: strconv.FormatInt(id, 10), Title: subject, Detail: to,
			Created: updated, Group: attention.NeedsDecision,
			Actions: []attention.Action{{Kind: "review", Label: "Review"}},
		})
	}
	return items, rows.Err()
}

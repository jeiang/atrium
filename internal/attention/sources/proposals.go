package sources

import (
	"context"
	"strconv"

	"github.com/jeiang/atrium/internal/attention"
	"github.com/jeiang/atrium/internal/db"
)

type proposals struct{}

func Proposals() attention.Source { return proposals{} }

func (proposals) Name() string { return "proposals" }

func (proposals) Items(ctx context.Context, q db.Querier, _ float64) ([]attention.Item, error) {
	rows, err := q.QueryContext(ctx, "SELECT id, kind, title, created_ts FROM proposals WHERE status = 'pending' ORDER BY created_ts")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []attention.Item
	for rows.Next() {
		var id int64
		var kind, title string
		var created float64
		if err := rows.Scan(&id, &kind, &title, &created); err != nil {
			return nil, err
		}
		items = append(items, attention.Item{
			Kind: "proposal", SourceID: strconv.FormatInt(id, 10), Title: title, Detail: kind,
			Created: created, Group: attention.NeedsDecision,
			Actions: []attention.Action{{Kind: "approve", Label: "Approve"}, {Kind: "dismiss", Label: "Dismiss"}},
		})
	}
	return items, rows.Err()
}

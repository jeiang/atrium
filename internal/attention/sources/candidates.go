package sources

import (
	"context"
	"strconv"

	"github.com/jeiang/atrium/internal/attention"
	"github.com/jeiang/atrium/internal/db"
)

type candidates struct{}

func Candidates() attention.Source { return candidates{} }

func (candidates) Name() string { return "candidates" }

func (candidates) Items(ctx context.Context, q db.Querier, _ float64) ([]attention.Item, error) {
	rows, err := q.QueryContext(ctx, "SELECT message_id, reason, ts FROM event_candidates WHERE status = 'pending' ORDER BY ts")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var items []attention.Item
	for rows.Next() {
		var id int64
		var reason string
		var ts float64
		if err := rows.Scan(&id, &reason, &ts); err != nil {
			return nil, err
		}
		items = append(items, attention.Item{
			Kind: "event_candidate", SourceID: strconv.FormatInt(id, 10), Title: "Event candidate", Detail: reason,
			Created: ts, Group: attention.NeedsDecision,
			Actions: []attention.Action{{Kind: "review", Label: "Review"}},
		})
	}
	return items, rows.Err()
}

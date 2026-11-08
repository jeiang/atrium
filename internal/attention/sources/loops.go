package sources

import (
	"context"

	"github.com/jeiang/atrium/internal/attention"
	"github.com/jeiang/atrium/internal/db"
)

type loops struct{}

func Loops() attention.Source { return loops{} }

func (loops) Name() string { return "loops" }

func (loops) Items(ctx context.Context, q db.Querier, now float64) ([]attention.Item, error) {
	rows, err := q.QueryContext(ctx,
		"SELECT l.thread_key, l.state, l.subject, l.counterpart, l.since FROM open_loops l "+
			"LEFT JOIN loop_overrides o ON o.thread_key = l.thread_key "+
			"WHERE COALESCE(o.dismissed, 0) = 0 AND (o.snoozed_until IS NULL OR o.snoozed_until <= ?) ORDER BY l.since", now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []attention.Item
	for rows.Next() {
		var key, state, subject, counterpart string
		var since float64
		if err := rows.Scan(&key, &state, &subject, &counterpart, &since); err != nil {
			return nil, err
		}
		item := attention.Item{Kind: "loop", SourceID: key, Title: subject, Detail: counterpart, Created: since}
		if state == "waiting_on_them" {
			item.Group = attention.Waiting
		} else {
			item.Group = attention.NeedsDecision
			item.Actions = []attention.Action{{Kind: "reply", Label: "Reply"}, {Kind: "dismiss", Label: "Dismiss"}}
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

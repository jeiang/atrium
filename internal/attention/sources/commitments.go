package sources

import (
	"context"
	"strconv"
	"time"

	"github.com/jeiang/atrium/internal/attention"
	"github.com/jeiang/atrium/internal/db"
)

type commitments struct{}

func Commitments() attention.Source { return commitments{} }

func (commitments) Name() string { return "commitments" }

func (commitments) Items(ctx context.Context, q db.Querier, now float64) ([]attention.Item, error) {
	rows, err := q.QueryContext(ctx,
		"SELECT id, text, due, created_ts FROM commitments WHERE owner = 'me' AND status = 'open' AND due IS NOT NULL ORDER BY due")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []attention.Item
	for rows.Next() {
		var id int64
		var text, due string
		var created float64
		if err := rows.Scan(&id, &text, &due, &created); err != nil {
			return nil, err
		}
		when, err := time.Parse("2006-01-02", due)
		if err != nil {
			continue
		}
		dueAt := float64(when.Unix())
		group := attention.DueSoon
		if dueAt+86400 <= now {
			group = attention.Overdue
		} else if dueAt > now+7*86400 {
			continue
		}
		items = append(items, attention.Item{
			Kind: "commitment", SourceID: strconv.FormatInt(id, 10), Title: text, Detail: due,
			Due: dueAt, Created: created, Group: group,
			Actions: []attention.Action{{Kind: "done", Label: "Done"}, {Kind: "dismiss", Label: "Dismiss"}, {Kind: "snooze", Label: "Snooze"}},
		})
	}
	return items, rows.Err()
}

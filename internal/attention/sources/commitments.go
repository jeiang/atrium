package sources

import (
	"context"
	"strconv"
	"time"

	"github.com/jeiang/atrium/internal/attention"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/mail/openloops"
)

type commitments struct{}

func Commitments() attention.Source { return commitments{} }

func (commitments) Name() string { return "commitments" }

func (commitments) Items(ctx context.Context, q db.Querier, now float64) ([]attention.Item, error) {
	found, err := openloops.AttentionCommitments(ctx, q)
	if err != nil {
		return nil, err
	}
	var items []attention.Item
	for _, c := range found {
		when, err := time.Parse("2006-01-02", c.Due)
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
			Kind: "commitment", SourceID: strconv.FormatInt(c.ID, 10), Title: c.Text, Detail: c.Due,
			Due: dueAt, Created: c.CreatedTS, Group: group,
			Actions: []attention.Action{{Kind: "done", Label: "Done"}, {Kind: "dismiss", Label: "Dismiss"}, {Kind: "snooze", Label: "Snooze"}},
		})
	}
	return items, nil
}

package tasks

import (
	"context"
	"strconv"
	"time"

	"github.com/jeiang/atrium/internal/attention"
	"github.com/jeiang/atrium/internal/db"
)

const attentionKind = "task"

func AttentionSource() attention.Source { return attentionSource{} }

type attentionSource struct{}

func (attentionSource) Name() string { return "tasks" }

func (attentionSource) Items(ctx context.Context, q db.Querier, now float64) ([]attention.Item, error) {
	rows, err := q.QueryContext(ctx, "SELECT "+columns+" FROM tasks WHERE status = 'open' AND due IS NOT NULL AND due <> '' ORDER BY due, id")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var items []attention.Item
	for rows.Next() {
		t, err := scan(rows)
		if err != nil {
			return nil, err
		}
		due, ok := dueAt(*t.Due)
		if !ok {
			continue
		}
		group := attention.Upcoming
		if due < now {
			group = attention.Overdue
		} else if due < now+7*86400 {
			group = attention.DueSoon
		}
		detail := "due " + *t.Due
		if t.Priority != "" && t.Priority != "normal" {
			detail += " · " + t.Priority
		}
		items = append(items, attention.Item{
			Kind: attentionKind, SourceID: strconv.FormatInt(t.ID, 10),
			Title: t.Title, Detail: detail, Due: due, Created: t.CreatedTS, Group: group,
			Actions: []attention.Action{
				{Kind: "done", Label: "Done"},
				{Kind: "dismiss", Label: "Dismiss"},
				{Kind: "snooze", Label: "Snooze"},
			},
		})
	}
	return items, rows.Err()
}

func dueAt(s string) (float64, bool) {
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04", "2006-01-02 15:04", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return float64(t.Unix()), true
		}
	}
	return 0, false
}

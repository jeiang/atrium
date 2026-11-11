package sources

import (
	"context"

	"github.com/jeiang/atrium/internal/attention"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/mail/openloops"
)

type loops struct{}

func Loops() attention.Source { return loops{} }

func (loops) Name() string { return "loops" }

func (loops) Items(ctx context.Context, q db.Querier, now float64) ([]attention.Item, error) {
	found, err := openloops.AttentionLoops(ctx, q, now)
	if err != nil {
		return nil, err
	}
	items := make([]attention.Item, 0, len(found))
	for _, l := range found {
		item := attention.Item{Kind: "loop", SourceID: l.ThreadKey, Title: l.Subject, Detail: l.Counterpart, Created: l.Since}
		if l.State == "waiting_on_them" {
			item.Group = attention.Waiting
		} else {
			item.Group = attention.NeedsDecision
			item.Actions = []attention.Action{{Kind: "reply", Label: "Reply"}, {Kind: "dismiss", Label: "Dismiss"}}
		}
		items = append(items, item)
	}
	return items, nil
}

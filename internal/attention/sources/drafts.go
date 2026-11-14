package sources

import (
	"context"
	"strconv"

	"github.com/jeiang/atrium/internal/attention"
	"github.com/jeiang/atrium/internal/db"
	draftq "github.com/jeiang/atrium/internal/mail/drafts"
)

type drafts struct{}

func Drafts() attention.Source { return drafts{} }

func (drafts) Name() string { return "drafts" }

func (drafts) Items(ctx context.Context, q db.Querier, _ float64) ([]attention.Item, error) {
	found, err := draftq.AttentionDrafts(ctx, q)
	if err != nil {
		return nil, err
	}
	items := make([]attention.Item, 0, len(found))
	for _, d := range found {
		items = append(items, attention.Item{
			Kind: "draft", SourceID: strconv.FormatInt(d.ID, 10), Title: d.Subject, Detail: d.To,
			Created: d.UpdatedAt, Group: attention.NeedsDecision,
			Actions: []attention.Action{{Kind: "review", Label: "Review"}},
		})
	}
	return items, nil
}

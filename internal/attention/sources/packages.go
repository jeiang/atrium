package sources

import (
	"context"
	"strconv"

	"github.com/jeiang/atrium/internal/attention"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/mail/tracking"
)

type packages struct{}

func Packages() attention.Source { return packages{} }

func (packages) Name() string { return "packages" }

func (packages) Items(ctx context.Context, q db.Querier, _ float64) ([]attention.Item, error) {
	found, err := tracking.AttentionPackages(ctx, q)
	if err != nil {
		return nil, err
	}
	items := make([]attention.Item, 0, len(found))
	for _, p := range found {
		title := p.Merchant
		if title == "" {
			title = "Package"
		}
		items = append(items, attention.Item{
			Kind: "package", SourceID: strconv.FormatInt(p.ID, 10), Title: title, Detail: p.Status + " " + p.TrackingNumber,
			Created: p.FirstSeen, Group: attention.Upcoming,
		})
	}
	return items, nil
}

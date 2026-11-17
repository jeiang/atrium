package packagesmodule

import (
	"context"
	"strconv"

	"github.com/jeiang/atrium/internal/apps/packages/tracking"
	"github.com/jeiang/atrium/internal/attention"
	"github.com/jeiang/atrium/internal/db"
)

type packagesSource struct{}

func attentionSource() attention.Source { return packagesSource{} }

func (packagesSource) Name() string { return "packages" }

func (packagesSource) Items(ctx context.Context, q db.Querier, _ float64) ([]attention.Item, error) {
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

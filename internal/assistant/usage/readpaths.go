package usage

import (
	"context"

	"github.com/jeiang/atrium/internal/assistant/store"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/db/readpaths"
)

func init() {
	readpaths.Register(readpaths.Path{
		Name:   "usage.summary",
		Golden: "usage.summary",
		Run: func(ctx context.Context, d *db.DB) (any, error) {
			loc, err := store.GoldenLocation()
			if err != nil {
				return nil, err
			}
			return MonthSummary(ctx, d.Read, store.GoldenNow, loc)
		},
	})
	readpaths.Register(readpaths.Path{
		Name:   "usage.by_kind",
		Golden: "usage.by_kind",
		Run: func(ctx context.Context, d *db.DB) (any, error) {
			loc, err := store.GoldenLocation()
			if err != nil {
				return nil, err
			}
			return ByKind(ctx, d.Read, store.GoldenNow, loc)
		},
	})
}

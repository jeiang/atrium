package readpaths_test

import (
	"context"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/db/readpaths"
	"github.com/jeiang/atrium/internal/proposals"
)

func init() {
	readpaths.Register(readpaths.Path{
		Name:   "proposals.List",
		Golden: "proposals.list",
		Run: func(ctx context.Context, d *db.DB) (any, error) {
			return proposals.List(ctx, d.Read, "", "")
		},
	})
	readpaths.Register(readpaths.Path{
		Name:   "proposals.CountsByStatus",
		Golden: "proposals.counts_by_status",
		Run: func(ctx context.Context, d *db.DB) (any, error) {
			return proposals.CountsByStatus(ctx, d.Read)
		},
	})
}

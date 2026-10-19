package notify

import (
	"context"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/db/readpaths"
)

func init() {
	readpaths.Register(readpaths.Path{
		Name:   "notify.Recent",
		Golden: "notify.recent",
		Run: func(ctx context.Context, d *db.DB) (any, error) {
			return Recent(ctx, d.Read, 100)
		},
	})
}

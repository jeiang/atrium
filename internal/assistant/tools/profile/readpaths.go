package profiletools

import (
	"context"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/db/readpaths"
)

func init() {
	readpaths.Register(readpaths.Path{
		Name:   "profile.Entries",
		Golden: "profile.entries",
		Run: func(ctx context.Context, d *db.DB) (any, error) {
			return Entries(ctx, d.Read)
		},
	})
}

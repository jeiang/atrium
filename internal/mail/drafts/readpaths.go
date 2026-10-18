package drafts

import (
	"context"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/db/readpaths"
)

func init() {
	readpaths.Register(readpaths.Path{
		Name:   "drafts.list_drafts",
		Golden: "drafts.list_drafts",
		Run: func(ctx context.Context, d *db.DB) (any, error) {
			return (&Store{DB: d}).List(ctx, "", 100)
		},
	})
}

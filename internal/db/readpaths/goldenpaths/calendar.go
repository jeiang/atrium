package readpaths_test

import (
	"context"

	"github.com/jeiang/atrium/internal/calendar/candidates"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/db/readpaths"
)

func init() {
	readpaths.Register(readpaths.Path{
		Name:   "candidates.Queued",
		Golden: "event_candidates.queued",
		Run: func(ctx context.Context, d *db.DB) (any, error) {
			return candidates.Queued(ctx, d.Read, candidates.DraftLimit)
		},
	})
}

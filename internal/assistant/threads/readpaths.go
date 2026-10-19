package threads

import (
	"context"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/db/readpaths"
)

func init() {
	for _, c := range []struct {
		name     string
		archived bool
	}{{"service.list_threads", false}, {"service.list_threads archived", true}} {
		readpaths.Register(readpaths.Path{
			Name:   "threads.list " + c.name,
			Golden: c.name,
			Run: func(ctx context.Context, d *db.DB) (any, error) {
				return ListThreads(ctx, d.Read, ListOpts{Limit: DefaultListLimit, Archived: c.archived})
			},
		})
	}
}

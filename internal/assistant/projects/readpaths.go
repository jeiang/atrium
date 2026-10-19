package projects

import (
	"context"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/db/readpaths"
)

func init() {
	readpaths.Register(readpaths.Path{
		Name:   "projects.list_projects",
		Golden: "projects.list_projects",
		Run: func(ctx context.Context, d *db.DB) (any, error) {
			return List(ctx, d.Read, false)
		},
	})
}

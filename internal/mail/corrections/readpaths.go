package corrections

import (
	"context"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/db/readpaths"
)

func init() {
	readpaths.Register(readpaths.Path{
		Name:   "corrections.List",
		Golden: "corrections.list_corrections",
		Run:    func(ctx context.Context, d *db.DB) (any, error) { return List(ctx, d.Read, "", DefaultListLimit) },
	})
}

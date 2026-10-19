package writes

import (
	"context"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/db/readpaths"
)

func init() {
	readpaths.Register(readpaths.Path{
		Name:   "writes.get profile",
		Golden: "writes.get profile",
		Run: func(ctx context.Context, d *db.DB) (any, error) {
			var id int64
			if err := d.Read.QueryRowContext(ctx, "SELECT id FROM agent_writes WHERE tool = 'profile_set' ORDER BY id LIMIT 1").Scan(&id); err != nil {
				return nil, err
			}
			return Get(ctx, d.Read, id)
		},
	})
}

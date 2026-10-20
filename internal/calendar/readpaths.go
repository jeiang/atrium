package calendar

import (
	"context"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/db/readpaths"
)

func init() {
	readpaths.Register(readpaths.Path{
		Name:   "calendar.FindTracked",
		Golden: "calendar.find_tracked",
		Run: func(ctx context.Context, d *db.DB) (any, error) {
			var uid string
			if err := d.Read.QueryRowContext(ctx, "SELECT uid FROM agent_events WHERE deleted_ts IS NULL ORDER BY uid LIMIT 1").Scan(&uid); err != nil {
				return nil, err
			}
			return FindTracked(ctx, d.Read, uid)
		},
	})
}

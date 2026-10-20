package media

import (
	"context"
	"database/sql"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/db/readpaths"
)

func init() {
	readpaths.Register(readpaths.Path{
		Name:   "media.ListChannels",
		Golden: "channels.list_channels",
		Run: func(ctx context.Context, d *db.DB) (any, error) {
			return ListChannels(ctx, d.Read)
		},
	})
	readpaths.Register(readpaths.Path{
		Name:   "media.ChannelStatus",
		Golden: "channels.status",
		Run: func(ctx context.Context, d *db.DB) (any, error) {
			return ChannelStatus(ctx, d.Read)
		},
	})
	readpaths.Register(readpaths.Path{
		Name:   "media.isSeen",
		Golden: "media.seen",
		Run: func(ctx context.Context, d *db.DB) (any, error) {
			rows, err := d.Read.QueryContext(ctx, "SELECT hash FROM media_seen ORDER BY hash")
			if err != nil {
				return nil, err
			}
			hashes, err := db.Collect(rows, func(r *sql.Rows) (string, error) {
				var h string
				return h, r.Scan(&h)
			})
			if err != nil {
				return nil, err
			}
			seen := map[string]bool{}
			for _, h := range hashes {
				seen[h], err = isSeen(ctx, d.Read, h)
				if err != nil {
					return nil, err
				}
			}
			return seen, nil
		},
	})
}

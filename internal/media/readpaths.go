package media

import (
	"context"

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
}

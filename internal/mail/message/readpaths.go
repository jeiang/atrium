package message

import (
	"context"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/db/readpaths"
)

func init() {
	for _, key := range []string{"dentist", "trip"} {
		readpaths.Register(readpaths.Path{
			Name:   "message.get_message " + key,
			Golden: "message.get_message " + key,
			Run: func(ctx context.Context, d *db.DB) (any, error) {
				cfg, err := config.Load(config.OS)
				if err != nil {
					return nil, err
				}
				var id int64
				if err := d.Read.QueryRowContext(ctx,
					"SELECT id FROM messages WHERE msgid = ? ORDER BY gone_at IS NULL DESC, id DESC",
					key+"@golden.example").Scan(&id); err != nil {
					return nil, err
				}
				return Get(ctx, d.Read, cfg.CacheDir, id)
			},
		})
	}
}

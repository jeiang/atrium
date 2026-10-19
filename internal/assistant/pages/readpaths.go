package pages

import (
	"context"

	"github.com/jeiang/atrium/internal/assistant/service"
	"github.com/jeiang/atrium/internal/assistant/store"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/db/readpaths"
)

func goldenThread(ctx context.Context, d *db.DB, title string) (int64, error) {
	var id int64
	err := d.Read.QueryRowContext(ctx, "SELECT MIN(id) FROM threads WHERE title = ? AND kind = 'chat'", title).Scan(&id)
	return id, err
}

func init() {
	for _, c := range []struct{ name, title string }{
		{"service.thread_view", "Plan my week"},
		{"service.thread_view compacting", "Long research"},
	} {
		readpaths.Register(readpaths.Path{
			Name:   "pages " + c.name,
			Golden: c.name,
			Run: func(ctx context.Context, d *db.DB) (any, error) {
				id, err := goldenThread(ctx, d, c.title)
				if err != nil {
					return nil, err
				}
				v, err := service.View(ctx, d.Read, id)
				if err != nil {
					return nil, err
				}
				return threadViewDict(v)
			},
		})
	}
	readpaths.Register(readpaths.Path{
		Name:   "pages service.run_view",
		Golden: "service.run_view",
		Run: func(ctx context.Context, d *db.DB) (any, error) {
			id, err := goldenThread(ctx, d, "Plan my week")
			if err != nil {
				return nil, err
			}
			var runID int64
			if err := d.Read.QueryRowContext(ctx, "SELECT MAX(id) FROM runs WHERE thread_id = ?", id).Scan(&runID); err != nil {
				return nil, err
			}
			v, err := service.ViewRun(ctx, d.Read, runID)
			if err != nil {
				return nil, err
			}
			return v.Dict(ctx, d.Read)
		},
	})
	readpaths.Register(readpaths.Path{
		Name:   "pages workspace.view",
		Golden: "workspace.view",
		Run: func(ctx context.Context, d *db.DB) (any, error) {
			id, err := goldenThread(ctx, d, "Plan my week")
			if err != nil {
				return nil, err
			}
			return workView(ctx, d.Read, id, store.GoldenNow)
		},
	})
}

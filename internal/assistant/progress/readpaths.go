package progress

import (
	"context"
	"database/sql"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/db/readpaths"
)

func init() {
	readpaths.Register(readpaths.Path{
		Name:   "progress.Since",
		Golden: "progress.since",
		Run: func(ctx context.Context, d *db.DB) (any, error) {
			rows, err := d.Read.QueryContext(ctx, "SELECT id FROM runs WHERE parent_run_id IS NULL AND id IN (SELECT run_id FROM run_events) ORDER BY id")
			if err != nil {
				return nil, err
			}
			ids, err := db.Collect(rows, func(r *sql.Rows) (int64, error) {
				var id int64
				err := r.Scan(&id)
				return id, err
			})
			if err != nil {
				return nil, err
			}
			type entry struct {
				RunID  int64   `json:"run_id"`
				Events []Event `json:"events"`
			}
			out := []entry{}
			for _, id := range ids {
				events, err := Since(ctx, d.Read, id, 0)
				if err != nil {
					return nil, err
				}
				out = append(out, entry{id, events})
			}
			return out, nil
		},
	})
}

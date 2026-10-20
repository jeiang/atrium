package context

import (
	gocontext "context"
	"database/sql"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/db/readpaths"
)

func threadIDs(ctx gocontext.Context, d *db.DB, query string) ([]int64, error) {
	rows, err := d.Read.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (int64, error) {
		var id int64
		err := r.Scan(&id)
		return id, err
	})
}

func init() {
	readpaths.Register(readpaths.Path{
		Name:   "context.LedgerRows",
		Golden: "context.ledger_rows",
		Run: func(ctx gocontext.Context, d *db.DB) (any, error) {
			ids, err := threadIDs(ctx, d, "SELECT DISTINCT r.thread_id FROM context_ledger l JOIN runs r ON r.id = l.run_id ORDER BY r.thread_id")
			if err != nil {
				return nil, err
			}
			type entry struct {
				ThreadID int64       `json:"thread_id"`
				Rows     []LedgerRow `json:"rows"`
			}
			out := []entry{}
			for _, id := range ids {
				ledger, err := LedgerRows(ctx, d.Read, id)
				if err != nil {
					return nil, err
				}
				out = append(out, entry{id, ledger.Rows})
			}
			return out, nil
		},
	})
	readpaths.Register(readpaths.Path{
		Name:   "context.GetProjection",
		Golden: "context.projection",
		Run: func(ctx gocontext.Context, d *db.DB) (any, error) {
			ids, err := threadIDs(ctx, d, "SELECT DISTINCT thread_id FROM context_projections ORDER BY thread_id")
			if err != nil {
				return nil, err
			}
			type entry struct {
				ThreadID   int64      `json:"thread_id"`
				Projection Projection `json:"projection"`
			}
			out := []entry{}
			for _, id := range ids {
				p, err := GetProjection(ctx, d.Read, id)
				if err != nil {
					return nil, err
				}
				out = append(out, entry{id, p})
			}
			return out, nil
		},
	})
}

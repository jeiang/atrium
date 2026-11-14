package audit

import (
	"context"
	"database/sql"

	"github.com/jeiang/atrium/internal/db"
)

type Row struct {
	ID     int64
	TS     float64
	Key    string
	Old    *string
	New    *string
	Actor  string
	Source string
}

func Before(ctx context.Context, q db.Querier, beforeTS float64, beforeID int64, limit int) ([]Row, error) {
	where, args := "", []any{}
	if beforeID > 0 {
		where, args = " WHERE (ts, id) < (?, ?)", []any{beforeTS, beforeID}
	}
	rows, err := q.QueryContext(ctx,
		"SELECT id, ts, key, old_value, new_value, actor, source FROM settings_audit"+where+" ORDER BY ts DESC, id DESC LIMIT ?", append(args, limit)...)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (Row, error) {
		var e Row
		err := r.Scan(&e.ID, &e.TS, &e.Key, &e.Old, &e.New, &e.Actor, &e.Source)
		return e, err
	})
}

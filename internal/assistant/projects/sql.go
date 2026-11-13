package projects

import (
	"context"

	"github.com/jeiang/atrium/internal/db"
)

func JoinClause(projectAlias, recordAlias string) string {
	return " LEFT JOIN projects " + projectAlias + " ON " + projectAlias + ".id = " + recordAlias + ".project_id"
}

func PrivateIDsSQL() string { return "SELECT id FROM projects WHERE private = 1" }

func PrivateThreadIDs(ctx context.Context, q db.Querier) (map[int64]bool, error) {
	rows, err := q.QueryContext(ctx, "SELECT id FROM threads WHERE project_id IN ("+PrivateIDsSQL()+")")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

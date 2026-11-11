package threads

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jeiang/atrium/internal/db"
)

type Tree struct {
	RootID int64
	CapUSD *float64
	IDs    []int64
}

func ThreadTree(ctx context.Context, q db.Querier, threadID int64) (Tree, error) {
	var root int64
	var capUSD *float64
	err := q.QueryRowContext(ctx,
		"WITH RECURSIVE up(id, parent) AS (SELECT id, parent_thread_id FROM threads WHERE id = ? "+
			"UNION SELECT t.id, t.parent_thread_id FROM threads t JOIN up ON t.id = up.parent), "+
			"root(id) AS (SELECT id FROM up WHERE parent IS NULL) "+
			"SELECT id, cost_cap_usd FROM threads WHERE id = (SELECT id FROM root)", threadID).Scan(&root, &capUSD)
	if errors.Is(err, sql.ErrNoRows) {
		return Tree{}, fmt.Errorf("threads: thread %d has no root", threadID)
	}
	if err != nil {
		return Tree{}, err
	}
	rows, err := q.QueryContext(ctx,
		"WITH RECURSIVE tree(id) AS (SELECT ? UNION SELECT t.id FROM threads t JOIN tree ON t.parent_thread_id = tree.id) SELECT id FROM tree", root)
	if err != nil {
		return Tree{}, err
	}
	ids, err := db.Collect(rows, func(r *sql.Rows) (int64, error) {
		var id int64
		return id, r.Scan(&id)
	})
	if err != nil {
		return Tree{}, err
	}
	return Tree{RootID: root, CapUSD: capUSD, IDs: ids}, nil
}

func ByKind(ctx context.Context, q db.Querier, kind Kind) ([]int64, error) {
	rows, err := q.QueryContext(ctx, "SELECT id FROM threads WHERE kind = ? ORDER BY id", string(kind))
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (int64, error) {
		var id int64
		return id, r.Scan(&id)
	})
}

func Children(ctx context.Context, q db.Querier, threadID int64) ([]int64, error) {
	rows, err := q.QueryContext(ctx, "SELECT id FROM threads WHERE parent_thread_id = ? AND deleted_at IS NULL ORDER BY id", threadID)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (int64, error) {
		var id int64
		return id, r.Scan(&id)
	})
}

package embed

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jeiang/atrium/internal/db"
)

type VectorHit struct {
	ID       int64
	Distance float64
}

func MessageCount(ctx context.Context, q db.Querier, idx Index) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+idx.MessageTable()).Scan(&n)
	return n, err
}

func MessageAccountCount(ctx context.Context, q db.Querier, idx Index, account string) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+idx.MessageTable()+" WHERE account = ?", account).Scan(&n)
	return n, err
}

func MessageEmbedding(ctx context.Context, q db.Querier, idx Index, id int64) ([]byte, error) {
	var blob []byte
	err := q.QueryRowContext(ctx, "SELECT embedding FROM "+idx.MessageTable()+" WHERE rowid = ?", id).Scan(&blob)
	return blob, err
}

func MessageVectorExists(ctx context.Context, q db.Querier, idx Index, id int64) (bool, error) {
	var one int
	err := q.QueryRowContext(ctx, "SELECT 1 FROM "+idx.MessageTable()+" WHERE rowid = ?", id).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func MessageVectors(ctx context.Context, q db.Querier, idx Index, ids []int64) (map[int64][]byte, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	marks := strings.TrimSuffix(strings.Repeat(",?", len(ids)-1), ",")
	rows, err := q.QueryContext(ctx, "SELECT rowid, embedding FROM "+idx.MessageTable()+" WHERE rowid IN (?"+marks+")", args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make(map[int64][]byte, len(ids))
	for rows.Next() {
		var id int64
		var blob []byte
		if err := rows.Scan(&id, &blob); err != nil {
			return nil, err
		}
		out[id] = blob
	}
	return out, rows.Err()
}

func MessageNeighbors(ctx context.Context, q db.Querier, idx Index, blob []byte, k int, account string) ([]VectorHit, error) {
	rows, err := q.QueryContext(ctx,
		"SELECT rowid, distance FROM "+idx.MessageTable()+" WHERE embedding MATCH ? AND k = ? AND account = ?", blob, k, account)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (VectorHit, error) {
		var h VectorHit
		err := r.Scan(&h.ID, &h.Distance)
		return h, err
	})
}

func MessageIDs(ctx context.Context, q db.Querier, idx Index, blob []byte, k int, account *string) ([]int64, error) {
	query := "SELECT rowid FROM " + idx.MessageTable() + " WHERE embedding MATCH ? AND k = ?"
	args := []any{blob, k}
	if account != nil {
		query += " AND account = ?"
		args = append(args, *account)
	}
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (int64, error) {
		var id int64
		err := r.Scan(&id)
		return id, err
	})
}

func MessageRank(ctx context.Context, q db.Querier, idx Index, blob []byte, candidates []int64, limit int) ([]int64, error) {
	if len(candidates) == 0 {
		return nil, nil
	}
	encoded, err := json.Marshal(candidates)
	if err != nil {
		return nil, err
	}
	rows, err := q.QueryContext(ctx,
		"SELECT rowid FROM "+idx.MessageTable()+" WHERE rowid IN (SELECT value FROM json_each(?)) ORDER BY vec_distance_cosine(embedding, ?) LIMIT ?",
		encoded, blob, limit)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (int64, error) {
		var id int64
		err := r.Scan(&id)
		return id, err
	})
}

func KBCount(ctx context.Context, q db.Querier, idx Index) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+idx.KBTable()).Scan(&n)
	return n, err
}

func KBHits(ctx context.Context, q db.Querier, idx Index, blob []byte, k int) ([]VectorHit, error) {
	rows, err := q.QueryContext(ctx, "SELECT rowid, distance FROM "+idx.KBTable()+" WHERE embedding MATCH ? AND k = ? ORDER BY distance", blob, k)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (VectorHit, error) {
		var h VectorHit
		err := r.Scan(&h.ID, &h.Distance)
		return h, err
	})
}

func KBIDs(ctx context.Context, q db.Querier, idx Index, blob []byte, k int) ([]int64, error) {
	rows, err := q.QueryContext(ctx, "SELECT rowid FROM "+idx.KBTable()+" WHERE embedding MATCH ? AND k = ? ORDER BY distance", blob, k)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (int64, error) {
		var id int64
		err := r.Scan(&id)
		return id, err
	})
}

func NotionCount(ctx context.Context, q db.Querier, idx Index) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+idx.NotionTable()).Scan(&n)
	return n, err
}

func NotionHits(ctx context.Context, q db.Querier, idx Index, blob []byte, k int, visibility string) ([]VectorHit, error) {
	rows, err := q.QueryContext(ctx,
		"SELECT rowid, distance FROM "+idx.NotionTable()+" WHERE embedding MATCH ? AND k = ? AND visibility = ?", blob, k, visibility)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (VectorHit, error) {
		var h VectorHit
		err := r.Scan(&h.ID, &h.Distance)
		return h, err
	})
}

func NotionEmbedding(ctx context.Context, q db.Querier, idx Index, rid int64) ([]byte, error) {
	var blob []byte
	err := q.QueryRowContext(ctx, "SELECT embedding FROM "+idx.NotionTable()+" WHERE rowid = ?", rid).Scan(&blob)
	return blob, err
}

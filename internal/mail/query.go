package mail

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"

	"github.com/jeiang/atrium/internal/db"
)

func Subject(ctx context.Context, q db.Querier, id int64) (string, error) {
	var subject string
	err := q.QueryRowContext(ctx, "SELECT COALESCE(subject, '') FROM messages WHERE id = ?", id).Scan(&subject)
	return subject, err
}

func MessageCount(ctx context.Context, q db.Querier, id int64) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM messages WHERE id = ?", id).Scan(&n)
	return n, err
}

func Recipients(ctx context.Context, q db.Querier, ids []int64) (map[int64]string, error) {
	list := make([]any, len(ids))
	for i, id := range ids {
		list[i] = id
	}
	encoded, err := json.Marshal(list)
	if err != nil {
		return nil, err
	}
	rows, err := q.QueryContext(ctx, "SELECT id, to_addrs FROM messages WHERE id IN (SELECT value FROM json_each(?))", encoded)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]string, len(ids))
	for rows.Next() {
		var id int64
		var to sql.NullString
		if err := rows.Scan(&id, &to); err != nil {
			_ = rows.Close()
			return nil, err
		}
		out[id] = to.String
	}
	return out, rows.Err()
}

type MessageInfo struct {
	ID       int64
	Account  string
	Subject  string
	FromAddr string
	DateTS   float64
}

func MessageInfos(ctx context.Context, q db.Querier, ids []int64) ([]MessageInfo, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	marks := strings.Repeat(",?", len(ids)-1)
	rows, err := q.QueryContext(ctx, "SELECT id, account, subject, from_addr, date_ts FROM messages WHERE id IN (?"+marks+")", args...)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (MessageInfo, error) {
		var m MessageInfo
		err := r.Scan(&m.ID, &m.Account, &m.Subject, &m.FromAddr, &m.DateTS)
		return m, err
	})
}

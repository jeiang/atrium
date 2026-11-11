package mail

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"
	"strconv"
	"strings"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/ledger"
)

func FromClause(alias string) string { return "messages " + alias }

func JoinClause(alias, on string) string { return " JOIN messages " + alias + " ON " + on }

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

type unresolvedAction struct {
	UIDs           []uint32                   `json:"uids"`
	Copied         map[string]json.RawMessage `json:"copied"`
	UIDValidity    uint32                     `json:"uidvalidity"`
	SrcUIDValidity uint32                     `json:"src_uidvalidity"`
	IMAPName       string                     `json:"imap_name"`
	SrcIMAP        string                     `json:"src_imap"`
}

func (a unresolvedAction) matches(uid, validity uint32, imapName string) bool {
	srcValidity := a.SrcUIDValidity
	if srcValidity == 0 {
		srcValidity = a.UIDValidity
	}
	srcName := a.SrcIMAP
	if srcName == "" {
		srcName = a.IMAPName
	}
	if srcValidity == validity && srcName == imapName && slices.Contains(a.UIDs, uid) {
		return true
	}
	if a.SrcUIDValidity != validity || a.SrcIMAP != imapName {
		return false
	}
	_, copied := a.Copied[strconv.FormatUint(uint64(uid), 10)]
	return copied
}

func UnresolvedMessages(ctx context.Context, q db.Querier, account string) (map[int64]bool, error) {
	effects, err := ledger.UnresolvedMailEffects(ctx, q, account)
	if err != nil {
		return nil, err
	}
	out := map[int64]bool{}
	if len(effects) == 0 {
		return out, nil
	}
	type unresolved struct {
		messageID *int64
		action    unresolvedAction
	}
	parsed := make([]unresolved, 0, len(effects))
	for _, e := range effects {
		var a unresolvedAction
		if err := json.Unmarshal(e.Action, &a); err != nil {
			continue
		}
		parsed = append(parsed, unresolved{messageID: e.MessageID, action: a})
	}
	rows, err := q.QueryContext(ctx, "SELECT m.id, m.uid, m.uidvalidity, mb.imap_name FROM messages m "+
		"JOIN mailboxes mb ON mb.id = m.mailbox_id WHERE m.account = ?", account)
	if err != nil {
		return nil, err
	}
	if _, err := db.Collect(rows, func(r *sql.Rows) (struct{}, error) {
		var id int64
		var uid, validity uint32
		var imapName string
		if err := r.Scan(&id, &uid, &validity, &imapName); err != nil {
			return struct{}{}, err
		}
		for _, u := range parsed {
			if u.messageID != nil && *u.messageID == id {
				out[id] = true
				return struct{}{}, nil
			}
			if u.action.matches(uid, validity, imapName) {
				out[id] = true
				return struct{}{}, nil
			}
		}
		return struct{}{}, nil
	}); err != nil {
		return nil, err
	}
	return out, nil
}

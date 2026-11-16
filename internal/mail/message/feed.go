package message

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"

	"github.com/jeiang/atrium/internal/communications"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/mail/mime"
)

const summaryColumns = "id, account, from_name, from_addr, to_addrs, subject, date_ts, seen, flagged, in_inbox, sent, " +
	"gone_at IS NOT NULL, msgid, in_reply_to, refs, gm_thrid"

func scanSummary(r interface{ Scan(...any) error }) (communications.Message, error) {
	var m communications.Message
	var to string
	var inReplyTo, refs, thread sql.NullString
	err := r.Scan(&m.ID, &m.Account, &m.From.Name, &m.From.Addr, &to, &m.Subject, &m.Date, &m.Flags.Seen, &m.Flags.Flagged,
		&m.Flags.InInbox, &m.Flags.Sent, &m.Flags.Gone, &m.Thread.MessageID, &inReplyTo, &refs, &thread)
	if err != nil {
		return m, err
	}
	m.Thread.InReplyTo, m.Thread.References, m.Thread.GmailThread = inReplyTo.String, refs.String, thread.String
	if err := json.Unmarshal([]byte(to), &m.To); err != nil {
		return m, err
	}
	return m, nil
}

func Summary(ctx context.Context, q Querier, id int64) (*communications.Message, error) {
	m, err := scanSummary(q.QueryRowContext(ctx, "SELECT "+summaryColumns+" FROM messages WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &m, nil
}

func Summaries(ctx context.Context, q Querier, query communications.Query) ([]communications.Message, error) {
	where := []string{"id > ?"}
	args := []any{query.AfterID}
	if query.Account != "" {
		where, args = append(where, "account = ?"), append(args, query.Account)
	}
	if query.IDs != nil {
		encoded, err := json.Marshal(query.IDs)
		if err != nil {
			return nil, err
		}
		where, args = append(where, "id IN (SELECT value FROM json_each(?))"), append(args, string(encoded))
	}
	if query.Inbox {
		where = append(where, "in_inbox = 1")
	}
	if query.Unseen {
		where = append(where, "seen = 0")
	}
	if query.Received {
		where = append(where, "sent = 0")
	}
	if query.Present {
		where = append(where, "gone_at IS NULL")
	}
	stmt := "SELECT " + summaryColumns + " FROM messages WHERE " + strings.Join(where, " AND ") + " ORDER BY id"
	if query.Limit > 0 {
		stmt += " LIMIT " + strconv.Itoa(query.Limit)
	}
	rows, err := q.QueryContext(ctx, stmt, args...)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (communications.Message, error) { return scanSummary(r) })
}

func parsed(ctx context.Context, q Querier, cacheDir string, id int64) (*mime.Message, error) {
	var raw sql.NullString
	err := q.QueryRowContext(ctx, "SELECT raw_path FROM messages WHERE id = ?", id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	path, ok := RawFile(cacheDir, raw.String)
	if !ok {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return mime.Parse(data), nil
}

func walkParts(part *mime.Message, id string, visit func(id string, part *mime.Message) bool) bool {
	if !visit(id, part) {
		return false
	}
	for i, sub := range part.Parts() {
		if !walkParts(sub, id+"."+strconv.Itoa(i+1), visit) {
			return false
		}
	}
	return true
}

func Parts(ctx context.Context, q Querier, cacheDir string, id int64) ([]communications.Part, error) {
	msg, err := parsed(ctx, q, cacheDir, id)
	if msg == nil || err != nil {
		return nil, err
	}
	var out []communications.Part
	walkParts(msg, "1", func(id string, part *mime.Message) bool {
		out = append(out, communications.Part{ID: id, ContentType: part.ContentType(), Params: part.Params(),
			Filename: part.Filename(), Attachment: part.IsAttachment()})
		return true
	})
	return out, nil
}

func PartData(ctx context.Context, q Querier, cacheDir string, id int64, partID string, limit int) ([]byte, error) {
	msg, err := parsed(ctx, q, cacheDir, id)
	if msg == nil || err != nil {
		return nil, err
	}
	var data []byte
	walkParts(msg, "1", func(id string, part *mime.Message) bool {
		if id != partID {
			return true
		}
		data = part.Payload()
		return false
	})
	if len(data) > limit {
		return nil, communications.ErrTooLarge
	}
	return data, nil
}

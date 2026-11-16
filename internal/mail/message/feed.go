package message

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	netmail "net/mail"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/jeiang/atrium/internal/communications"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/mail/mime"
)

const summaryColumns = "id, account, from_name, from_addr, to_addrs, subject, date_ts, seen, flagged, in_inbox, sent, " +
	"gone_at IS NOT NULL, msgid, in_reply_to, refs, gm_thrid, from_domain, list_id, precedence, category, esp, reply_to, labels, flags"

func IsDraft(labels, flagList string) (bool, error) {
	for _, value := range [...]string{labels, flagList} {
		if !strings.Contains(value, "Draft") {
			continue
		}
		var list []string
		if err := json.Unmarshal([]byte(value), &list); err != nil {
			return false, err
		}
		if slices.Contains(list, `\Draft`) {
			return true, nil
		}
	}
	return false, nil
}

func scanSummary(r interface{ Scan(...any) error }) (communications.Message, error) {
	var m communications.Message
	var to, labels, flagList string
	var inReplyTo, refs, thread sql.NullString
	err := r.Scan(&m.ID, &m.Account, &m.From.Name, &m.From.Addr, &to, &m.Subject, &m.Date, &m.Flags.Seen, &m.Flags.Flagged,
		&m.Flags.InInbox, &m.Flags.Sent, &m.Flags.Gone, &m.Thread.MessageID, &inReplyTo, &refs, &thread, &m.Mailing.Domain,
		&m.Mailing.ListID, &m.Mailing.Precedence, &m.Mailing.Category, &m.Mailing.ESP, &m.Mailing.ReplyTo, &labels, &flagList)
	if err != nil {
		return m, err
	}
	m.Thread.InReplyTo, m.Thread.References, m.Thread.GmailThread = inReplyTo.String, refs.String, thread.String
	if err := json.Unmarshal([]byte(to), &m.To); err != nil {
		return m, err
	}
	m.Flags.Draft, err = IsDraft(labels, flagList)
	return m, err
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
	if query.Since != 0 {
		where, args = append(where, "date_ts >= ?"), append(args, query.Since)
	}
	order := " ORDER BY id"
	if query.ByDate {
		order = " ORDER BY date_ts, id"
	}
	stmt := "SELECT " + summaryColumns + " FROM messages WHERE " + strings.Join(where, " AND ") + order
	if query.Limit > 0 {
		stmt += " LIMIT " + strconv.Itoa(query.Limit)
	}
	rows, err := q.QueryContext(ctx, stmt, args...)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (communications.Message, error) { return scanSummary(r) })
}

func Selves(ctx context.Context, q Querier) ([]string, error) {
	rows, err := q.QueryContext(ctx, "SELECT DISTINCT from_addr FROM messages WHERE sent = 1 AND from_addr != ''")
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (string, error) {
		var addr string
		return addr, r.Scan(&addr)
	})
}

func Asking(ctx context.Context, q Querier, id int64) (*bool, error) {
	var asking sql.NullInt64
	err := q.QueryRowContext(ctx, "SELECT person_asking FROM flag_decisions WHERE message_id = ?", id).Scan(&asking)
	if errors.Is(err, sql.ErrNoRows) || err == nil && !asking.Valid {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	value := asking.Int64 != 0
	return &value, nil
}

func Envelope(ctx context.Context, q Querier, cacheDir string, id int64) (communications.Envelope, error) {
	var raw sql.NullString
	err := q.QueryRowContext(ctx, "SELECT raw_path FROM messages WHERE id = ?", id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) || err == nil && raw.String == "" {
		return communications.Envelope{}, nil
	}
	if err != nil {
		return communications.Envelope{}, err
	}
	path, ok := RawFile(cacheDir, raw.String)
	if !ok {
		return communications.Envelope{}, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return communications.Envelope{}, nil
	}
	defer func() { _ = f.Close() }()
	msg := mime.Parse(header(f))
	return communications.Envelope{Found: true, To: addresses(msg.Addresses("To")), Cc: addresses(msg.Addresses("Cc")),
		ListUnsubscribe: msg.Header("List-Unsubscribe"), AutoSubmitted: msg.Header("Auto-Submitted")}, nil
}

func addresses(addrs []netmail.Address) []string {
	out := make([]string, 0, len(addrs))
	for _, a := range addrs {
		if a.Address != "" {
			out = append(out, a.Address)
		}
	}
	return out
}

func header(r io.Reader) []byte {
	data, _ := io.ReadAll(io.LimitReader(r, 256<<10))
	if i := bytes.Index(data, []byte("\r\n\r\n")); i >= 0 {
		return data[:i]
	}
	if i := bytes.Index(data, []byte("\n\n")); i >= 0 {
		return data[:i]
	}
	return data
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

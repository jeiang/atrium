package mailscan

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/httpx"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/mail/classify"
	"github.com/jeiang/atrium/internal/mail/message"
)

var log = logx.Logger("atrium.mailscan")

type Message struct {
	ID         int64
	Account    string
	DateTS     float64
	Subject    string
	FromName   string
	FromAddr   string
	FromDomain string
	Sent       bool
	Labels     string
	Flags      string
	Precedence string
	Category   string
	ListID     string
	ToAddrs    string
	ESP        string
	ReplyTo    string
	RawPath    string
}

type Spec struct {
	Kind       string
	Hint       string
	WindowDays int
	FetchLimit int
	Limit      int
	Paid       *classify.Deferral
	Eligible   func(Message) (bool, error)
	Judge      func(ctx context.Context, row Message, body string) (found int, err error)
}

type Result struct {
	Scanned int
	Found   int
}

func page(ctx context.Context, q db.Querier, now float64, spec Spec, after *Message) ([]Message, error) {
	where := "WHERE m.gone_at IS NULL AND m.date_ts >= ? AND (" + spec.Hint + ") AND NOT EXISTS " +
		"(SELECT 1 FROM messages o JOIN mail_scans s ON s.message_id = o.id " +
		"WHERE s.kind = ? AND o.account = m.account AND o.digest = m.digest)"
	args := []any{now - float64(spec.WindowDays)*86400, spec.Kind}
	if after != nil {
		where += " AND (m.date_ts < ? OR (m.date_ts = ? AND m.id < ?))"
		args = append(args, after.DateTS, after.DateTS, after.ID)
	}
	rows, err := q.QueryContext(ctx,
		"SELECT m.id, m.account, m.date_ts, m.subject, m.from_name, m.from_addr, m.from_domain, m.sent, "+
			"m.labels, m.flags, m.precedence, m.category, m.list_id, m.to_addrs, m.esp, m.reply_to, m.raw_path FROM messages m "+
			where+" ORDER BY m.date_ts DESC, m.id DESC LIMIT ?",
		append(args, spec.FetchLimit)...)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (Message, error) {
		var m Message
		var rawPath sql.NullString
		err := r.Scan(&m.ID, &m.Account, &m.DateTS, &m.Subject, &m.FromName, &m.FromAddr, &m.FromDomain, &m.Sent,
			&m.Labels, &m.Flags, &m.Precedence, &m.Category, &m.ListID, &m.ToAddrs, &m.ESP, &m.ReplyTo, &rawPath)
		m.RawPath = rawPath.String
		return m, err
	})
}

func Run(ctx context.Context, d *db.DB, now float64, spec Spec) (Result, error) {
	var result Result
	var after *Message
	examined, judged := 0, 0
	for {
		rows, err := page(ctx, d.Read, now, spec, after)
		if err != nil || len(rows) == 0 {
			return result, err
		}
		for _, row := range rows {
			if examined >= spec.FetchLimit || judged >= spec.Limit {
				return result, nil
			}
			after = &row
			if spec.Eligible != nil {
				ok, err := spec.Eligible(row)
				if err != nil {
					return result, err
				}
				if !ok {
					examined++
					continue
				}
			}
			counted, err := scan(ctx, d, now, spec, row, &result)
			if err != nil {
				return result, err
			}
			if counted {
				examined++
				judged++
			}
		}
	}
}

func scan(ctx context.Context, d *db.DB, now float64, spec Spec, row Message, result *Result) (bool, error) {
	body, err := message.Body(ctx, d.Read, row.ID)
	if err != nil {
		return false, err
	}
	found, err := spec.Judge(ctx, row, body)
	final, unanswered := classify.Unanswered(err)
	switch {
	case spec.Paid.Note(err):
		return true, nil
	case classify.Waiting(err):
		return false, nil
	case unanswered && !final:
		return true, nil
	case unanswered:
		found = 0
	case errors.Is(err, httpx.ErrPayload) || errors.Is(err, httpx.ErrMissing) || errors.Is(err, httpx.ErrShape):
		log.Warn(fmt.Sprintf("message %d: unusable %s answer: %s", row.ID, spec.Kind, httpx.Message(err)))
		found = 0
	case err != nil:
		return false, err
	}
	result.Scanned++
	result.Found += found
	_, err = d.Write.ExecContext(ctx, "INSERT OR REPLACE INTO mail_scans (kind, message_id, ts, found) VALUES (?, ?, ?, ?)",
		spec.Kind, row.ID, now, found > 0)
	return true, err
}

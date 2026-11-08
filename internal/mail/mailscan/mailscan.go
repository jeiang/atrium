package mailscan

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/httpx"
	"github.com/jeiang/atrium/internal/logx"
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
	Eligible   func(Message) (bool, error)
	Judge      func(ctx context.Context, row Message, body string) (found int, err error)
}

type Result struct {
	Scanned int
	Found   int
}

func candidates(ctx context.Context, q db.Querier, now float64, spec Spec) ([]Message, error) {
	rows, err := q.QueryContext(ctx,
		"SELECT m.id, m.account, m.date_ts, m.subject, m.from_name, m.from_addr, m.from_domain, m.sent, "+
			"m.labels, m.flags, m.precedence, m.category, m.list_id, m.to_addrs, m.esp, m.reply_to, m.raw_path FROM messages m "+
			"WHERE m.gone_at IS NULL AND m.date_ts >= ? AND ("+spec.Hint+") AND NOT EXISTS "+
			"(SELECT 1 FROM messages o JOIN mail_scans s ON s.message_id = o.id "+
			"WHERE s.kind = ? AND o.account = m.account AND o.digest = m.digest) "+
			"ORDER BY m.date_ts DESC LIMIT ?",
		now-float64(spec.WindowDays)*86400, spec.Kind, spec.FetchLimit)
	if err != nil {
		return nil, err
	}
	all, err := db.Collect(rows, func(r *sql.Rows) (Message, error) {
		var m Message
		var rawPath sql.NullString
		err := r.Scan(&m.ID, &m.Account, &m.DateTS, &m.Subject, &m.FromName, &m.FromAddr, &m.FromDomain, &m.Sent,
			&m.Labels, &m.Flags, &m.Precedence, &m.Category, &m.ListID, &m.ToAddrs, &m.ESP, &m.ReplyTo, &rawPath)
		m.RawPath = rawPath.String
		return m, err
	})
	if err != nil {
		return nil, err
	}
	if spec.Eligible == nil {
		return all[:min(len(all), spec.Limit)], nil
	}
	out := make([]Message, 0, min(len(all), spec.Limit))
	for _, m := range all {
		ok, err := spec.Eligible(m)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		out = append(out, m)
		if len(out) >= spec.Limit {
			break
		}
	}
	return out, nil
}

func Run(ctx context.Context, d *db.DB, now float64, spec Spec) (Result, error) {
	var result Result
	rows, err := candidates(ctx, d.Read, now, spec)
	if err != nil {
		return result, err
	}
	for _, row := range rows {
		body, err := message.Body(ctx, d.Read, row.ID)
		if err != nil {
			return result, err
		}
		found, err := spec.Judge(ctx, row, body)
		var unavailable *httpx.ServiceUnavailable
		switch {
		case errors.As(err, &unavailable):
			log.Warn(fmt.Sprintf("chat unavailable, deferring %s scan: %s", spec.Kind, unavailable))
			return result, nil
		case errors.Is(err, httpx.ErrPayload) || errors.Is(err, httpx.ErrMissing) || errors.Is(err, httpx.ErrShape):
			log.Warn(fmt.Sprintf("message %d: unusable %s answer: %s", row.ID, spec.Kind, httpx.Message(err)))
			found = 0
		case err != nil:
			return result, err
		}
		result.Scanned++
		result.Found += found
		if _, err := d.Write.ExecContext(ctx, "INSERT OR REPLACE INTO mail_scans (kind, message_id, ts, found) VALUES (?, ?, ?, ?)",
			spec.Kind, row.ID, now, found > 0); err != nil {
			return result, err
		}
	}
	return result, nil
}

package flags

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"regexp"

	"github.com/jeiang/atrium/internal/contacts"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/embed"
	"github.com/jeiang/atrium/internal/endpoint"
	"github.com/jeiang/atrium/internal/httpx"
	"github.com/jeiang/atrium/internal/llm"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/mail/message"
)

var (
	log = logx.Logger("atrium.flags")

	automatedLocal = regexp.MustCompile(`(?i)no[-_.]?reply|do[-_.]?not[-_.]?reply|notifications?|mailer-daemon|postmaster|bounces?`)
)

type row struct {
	ID       int64
	FromAddr string
	FromName string
	Subject  string
	ListID   string
}

func contactsFlag(row row, known map[string]struct{}) (bool, error) {
	if _, ok := known[row.FromAddr]; row.FromAddr == "" || !ok || row.ListID != "" {
		return false, nil
	}
	local, _, _ := strings.Cut(row.FromAddr, "@")
	automated, err := AutomatedLocal(local)
	return !automated && err == nil, err
}

func AutomatedLocal(local string) (bool, error) {
	return automatedLocal.MatchString(local), nil
}

type Judge func(ctx context.Context, url string, m llm.Message, body string) (llm.Flags, error)

func pending(ctx context.Context, d *db.DB, account string) ([]row, error) {
	rows, err := d.Read.QueryContext(ctx,
		"SELECT m.id, m.from_addr, m.from_name, m.subject, m.list_id FROM messages m WHERE m.account = ? AND m.in_inbox = 1 "+
			"AND m.seen = 0 AND m.sent = 0 AND m.gone_at IS NULL AND NOT EXISTS (SELECT 1 FROM flag_decisions f "+
			"WHERE f.message_id = m.id AND f.llm_flag IS NOT NULL) ORDER BY m.id", account)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (row, error) {
		var row row
		err := r.Scan(&row.ID, &row.FromAddr, &row.FromName, &row.Subject, &row.ListID)
		return row, err
	})
}

func nullable(ok bool, v bool) any {
	if !ok {
		return nil
	}
	return b2i(v)
}

func b2i(v bool) int {
	if v {
		return 1
	}
	return 0
}

func FlagAccount(ctx context.Context, d *db.DB, account string, judge Judge, now func() float64) (int, error) {
	if judge == nil {
		judge = llm.JudgeFlag
	}
	if now == nil {
		now = func() float64 { return float64(time.Now().UnixNano()) / 1e9 }
	}
	rows, err := pending(ctx, d, account)
	if err != nil || len(rows) == 0 {
		return 0, err
	}
	contactsDir, err := contacts.Dir(ctx, d.Read)
	if err != nil {
		return 0, err
	}
	chatURL, err := endpoint.ChatURL(ctx, d.Read)
	if err != nil {
		return 0, err
	}
	ctx, err = embed.WithModels(ctx, d.Read)
	if err != nil {
		return 0, err
	}
	known := map[string]struct{}{}
	for _, card := range contacts.Load(contactsDir) {
		for _, email := range card.Emails {
			known[email] = struct{}{}
		}
	}
	available := true
	done := 0
	for _, row := range rows {
		var verdict llm.Flags
		judged := false
		if available {
			body, err := message.Body(ctx, d.Read, row.ID)
			if err != nil {
				return done, err
			}
			verdict, err = judge(ctx, chatURL, llm.Message{Subject: row.Subject, FromName: row.FromName, FromAddr: row.FromAddr}, body)
			var unavailable *httpx.ServiceUnavailable
			switch {
			case errors.As(err, &unavailable):
				log.Warn(fmt.Sprintf("chat unavailable, deferring flag judgment: %s", unavailable))
				available = false
			case err != nil:
				return done, err
			default:
				judged = true
			}
		}
		match, err := contactsFlag(row, known)
		if err != nil {
			return done, err
		}
		_, err = d.Write.ExecContext(ctx,
			"INSERT OR REPLACE INTO flag_decisions (message_id, account, ts, contacts_match, person_asking, "+
				"deadline_or_payment_due, security_event, llm_flag) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
			row.ID, account, now(), b2i(match),
			nullable(judged, verdict.PersonAsking), nullable(judged, verdict.DeadlineOrPaymentDue),
			nullable(judged, verdict.SecurityEvent), nullable(judged, verdict.Flag))
		if err != nil {
			return done, err
		}
		done++
	}
	return done, nil
}

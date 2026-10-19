// Package flags is flags.py: the contacts rule and the logged LLM flag
// judgment recorded per unread inbox message in flag_decisions.
package flags

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/contacts"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/httpx"
	"github.com/jeiang/atrium/internal/llm"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/mail/router"
	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/rx"
)

var (
	log = logx.Logger("atrium.flags")

	automatedLocal = rx.MustCompile(
		`no[-_.]?reply|do[-_.]?not[-_.]?reply|notifications?|mailer-daemon|postmaster|bounces?`, rx.IgnoreCase)
)

// Contacts is the set of lower-cased addresses on the contacts' vCards.
type Contacts map[string]struct{}

// VCardEmails is vcard_emails: every EMAIL value that holds an "@", stripped and lower-cased.
func VCardEmails(text string) Contacts {
	found := Contacts{}
	for _, line := range py.SplitLines(contacts.Unfold(text)) {
		head, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		name, _, _ := strings.Cut(head, ";")
		if i := strings.LastIndex(name, "."); i >= 0 {
			name = name[i+1:]
		}
		if strings.ToUpper(name) == "EMAIL" && strings.Contains(value, "@") {
			found[py.Lower(py.Strip(value))] = struct{}{}
		}
	}
	return found
}

// LoadContacts is load_contacts: the addresses of every *.vcf under dir; an empty or missing dir has none.
func LoadContacts(dir string) Contacts {
	emails := Contacts{}
	if info, err := os.Stat(dir); dir == "" || err != nil || !info.IsDir() {
		return emails
	}
	for _, path := range contacts.VCFPaths(dir) {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		for e := range VCardEmails(contacts.ReadText(data)) {
			emails[e] = struct{}{}
		}
	}
	return emails
}

// Row is the part of a messages row the rule and the judge read.
type Row struct {
	ID       int64
	FromAddr string
	FromName string
	Subject  string
	ListID   string
}

// ContactsFlag is contacts_flag: a contact's address, not on a list, whose local part is no automated sender's.
func ContactsFlag(row Row, contacts Contacts) (bool, error) {
	if _, ok := contacts[row.FromAddr]; row.FromAddr == "" || !ok || row.ListID != "" {
		return false, nil
	}
	local, _, _ := strings.Cut(row.FromAddr, "@")
	automated, err := AutomatedLocal(local)
	return !automated && err == nil, err
}

// AutomatedLocal is AUTOMATED_LOCAL.search(local) is not None: whether the part of an address before "@" names an
// automated sender.
func AutomatedLocal(local string) (bool, error) {
	m, err := automatedLocal.Search(local)
	return m != nil, err
}

// Judge is llm.JudgeFlag's shape.
type Judge func(ctx context.Context, url string, m llm.Message, body string) (llm.Flags, error)

func pending(ctx context.Context, d *db.DB, account string) ([]Row, error) {
	rows, err := d.Read.QueryContext(ctx,
		"SELECT m.id, m.from_addr, m.from_name, m.subject, m.list_id FROM messages m WHERE m.account = ? AND m.in_inbox = 1 "+
			"AND m.seen = 0 AND m.sent = 0 AND m.gone_at IS NULL AND NOT EXISTS (SELECT 1 FROM flag_decisions f "+
			"WHERE f.message_id = m.id AND f.llm_flag IS NOT NULL) ORDER BY m.id", account)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (Row, error) {
		var row Row
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

// FlagAccount is flag_account: it records the contacts rule and, while the chat server answers, the LLM verdict
// for each unjudged unread inbox message of account, and returns how many it recorded. Once the server is
// unavailable the remaining rows are recorded with no verdict; any other judge error is returned. A nil judge is
// llm.JudgeFlag; a nil now is the wall clock.
func FlagAccount(ctx context.Context, d *db.DB, cfg config.Config, account string, judge Judge, now func() float64) (int, error) {
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
	known := LoadContacts(cfg.ContactsDir)
	available := true
	done := 0
	for _, row := range rows {
		var verdict llm.Flags
		judged := false
		if available {
			body, err := router.MessageBody(ctx, d.Read, row.ID)
			if err != nil {
				return done, err
			}
			verdict, err = judge(ctx, cfg.ChatURL, llm.Message{Subject: row.Subject, FromName: row.FromName, FromAddr: row.FromAddr}, body)
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
		match, err := ContactsFlag(row, known)
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

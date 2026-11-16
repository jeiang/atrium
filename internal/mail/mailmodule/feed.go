package mailmodule

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jeiang/atrium/internal/apps/mail/message"
	"github.com/jeiang/atrium/internal/communications"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/httpx"
	mailclassify "github.com/jeiang/atrium/internal/mail/classify"
	"github.com/jeiang/atrium/internal/mail/mailscan"
)

type feed struct {
	env config.Env
}

func (f feed) cacheDir() (string, error) {
	cfg, err := config.Load(f.env)
	return cfg.CacheDir, err
}

func (feed) Message(ctx context.Context, q db.Querier, id int64) (*communications.Message, error) {
	return message.Summary(ctx, q, id)
}

func (feed) Messages(ctx context.Context, q db.Querier, query communications.Query) ([]communications.Message, error) {
	return message.Summaries(ctx, q, query)
}

func (feed) Body(ctx context.Context, q db.Querier, id int64) (string, error) {
	return message.Body(ctx, q, id)
}

func (f feed) Parts(ctx context.Context, q db.Querier, id int64) ([]communications.Part, error) {
	dir, err := f.cacheDir()
	if err != nil {
		return nil, err
	}
	return message.Parts(ctx, q, dir, id)
}

func (f feed) Part(ctx context.Context, q db.Querier, id int64, part string, limit int) ([]byte, error) {
	dir, err := f.cacheDir()
	if err != nil {
		return nil, err
	}
	return message.PartData(ctx, q, dir, id, part, limit)
}

func (f feed) Envelope(ctx context.Context, q db.Querier, id int64) (communications.Envelope, error) {
	dir, err := f.cacheDir()
	if err != nil {
		return communications.Envelope{}, err
	}
	return message.Envelope(ctx, q, dir, id)
}

func (feed) Asking(ctx context.Context, q db.Querier, id int64) (*bool, error) {
	return message.Asking(ctx, q, id)
}

func (feed) Selves(ctx context.Context, q db.Querier) ([]string, error) {
	return message.Selves(ctx, q)
}

func (feed) Scan(ctx context.Context, d *db.DB, now float64, scan communications.Scan) (communications.ScanResult, error) {
	paid := scan.Paid
	if paid == nil {
		paid = &mailclassify.Deferral{}
	}
	result, err := mailscan.Run(ctx, d, now, mailscan.Spec{
		Kind: scan.Kind, Hint: "1", WindowDays: scan.WindowDays, FetchLimit: scan.FetchLimit, Limit: scan.Limit, Paid: paid,
		Eligible: func(m mailscan.Message) (bool, error) {
			found, err := scanned(m)
			if err != nil {
				return false, err
			}
			return scan.Eligible(found)
		},
		Judge: func(ctx context.Context, m mailscan.Message, body string) (int, error) {
			found, err := scanned(m)
			if err == nil {
				found.To, err = recipients(m.ToAddrs)
			}
			if err != nil {
				return 0, err
			}
			return scan.Judge(ctx, found, body)
		},
	})
	return communications.ScanResult{Scanned: result.Scanned, Found: result.Found}, err
}

func scanned(m mailscan.Message) (communications.Message, error) {
	draft, err := message.IsDraft(m.Labels, m.Flags)
	return communications.Message{ID: m.ID, Account: m.Account, From: communications.Address{Name: m.FromName, Addr: m.FromAddr},
		Subject: m.Subject, Date: m.DateTS, Flags: communications.Flags{Sent: m.Sent, Draft: draft},
		Mailing: communications.Mailing{Domain: m.FromDomain, ListID: m.ListID, Precedence: m.Precedence, Category: m.Category,
			ESP: m.ESP, ReplyTo: m.ReplyTo}}, err
}

func recipients(text string) ([]string, error) {
	if text == "" {
		text = "[]"
	}
	var out []string
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		return nil, fmt.Errorf("%w: to_addrs is not a list of strings", httpx.ErrShape)
	}
	return out, nil
}

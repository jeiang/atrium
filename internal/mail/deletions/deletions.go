package deletions

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/endpoint"
	"github.com/jeiang/atrium/internal/httpx"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/mailops"
	"github.com/jeiang/atrium/internal/mail/rules"
	"github.com/jeiang/atrium/internal/mail/scanutil"
	"github.com/jeiang/atrium/internal/ordmap"
	"github.com/jeiang/atrium/internal/proposals"
)

var log = logx.Logger("atrium.deletions")

const (
	Kind          = "deletion_rule"
	ReasonDeleted = "operator_deleted"
	ReasonOTP     = "otp"
)

type params struct {
	minGroup                     int
	minDeletedShare, otpMinShare float64
	otpOlderThanDays             int
	limits                       scanutil.Limits
}

func readParams(ctx context.Context, q db.Querier) (params, error) {
	r := endpoint.Read(ctx, q)
	p := params{
		minGroup:         r.Int(mail.KeyDeletionsMinGroup),
		minDeletedShare:  r.Float(mail.KeyDeletionsMinShare),
		otpMinShare:      r.Float(mail.KeyDeletionsOTPMinShare),
		otpOlderThanDays: r.Int(mail.KeyDeletionsOTPOlderDays),
	}
	if err := r.Err(); err != nil {
		return params{}, err
	}
	var err error
	p.limits, err = scanutil.ReadLimits(ctx, q)
	return p, err
}

type level struct {
	name, column, op string
}

var levels = []level{{"addr", "from_addr", "eq"}, {"domain", "from_domain", "suffix"}}

type row struct {
	rules.Message
}

func (r *row) column(name string) string {
	if name == "from_addr" {
		return r.FromAddr
	}
	return r.FromDomain
}

func (r *row) view() map[string]any {
	return scanutil.Message{ID: r.ID, Subject: r.Subject, FromName: r.FromName, FromAddr: r.FromAddr, DateTS: r.DateTS}.View()
}

func received(ctx context.Context, q proposals.Querier, accounts []string) ([]*row, error) {
	if len(accounts) == 0 {
		return nil, nil
	}
	args := make([]any, len(accounts))
	for i, account := range accounts {
		args[i] = account
	}
	rows, err := q.QueryContext(ctx, "SELECT "+rules.MessageColumns+" FROM messages WHERE sent = 0 AND account IN (?"+
		strings.Repeat(",?", len(accounts)-1)+") ORDER BY id", args...)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (*row, error) {
		var m row
		var err error
		m.Message, err = rules.ScanMessage(r)
		return &m, err
	})
}

func deletedIDs(ctx context.Context, q proposals.Querier) (map[int64]bool, error) {
	rows, err := q.QueryContext(ctx, "SELECT DISTINCT message_id FROM events WHERE kind = 'delete'")
	if err != nil {
		return nil, err
	}
	ids, err := db.Collect(rows, func(r *sql.Rows) (sql.NullInt64, error) {
		var id sql.NullInt64
		err := r.Scan(&id)
		return id, err
	})
	out := map[int64]bool{}
	for _, id := range ids {
		if id.Valid {
			out[id.Int64] = true
		}
	}
	return out, err
}

type candidate struct {
	Reason  string
	Level   level
	Account string
	Key     string
	Tmpl    string
	Rows    []*row
	Hits    int
}

type groupKey struct{ account, key, tmpl string }

func (p params) candidates(everyone []*row, deleted map[int64]bool) []candidate {
	var found []candidate
	claimed := map[int64]bool{}
	for _, lv := range levels {
		var g ordmap.Map[groupKey, []*row]
		for _, r := range everyone {
			if !claimed[r.ID] && r.column(lv.column) != "" && r.SubjectTmpl != "" {
				k := groupKey{r.Account, r.column(lv.column), r.SubjectTmpl}
				rows, _ := g.Get(k)
				g.Set(k, append(rows, r))
			}
		}
		for _, k := range g.Sorted(compareKeys) {
			rows, _ := g.Get(k)
			gone := 0
			for _, r := range rows {
				if deleted[r.ID] {
					gone++
				}
			}
			if len(rows) >= p.minGroup && float64(gone)/float64(len(rows)) >= p.minDeletedShare {
				for _, r := range rows {
					claimed[r.ID] = true
				}
				found = append(found, candidate{ReasonDeleted, lv, k.account, k.key, k.tmpl, rows, gone})
			}
		}
	}
	var otp ordmap.Map[groupKey, []*row]
	for _, r := range everyone {
		if r.FromDomain != "" && r.SubjectTmpl != "" {
			k := groupKey{r.Account, r.FromDomain, r.SubjectTmpl}
			rows, _ := otp.Get(k)
			otp.Set(k, append(rows, r))
		}
	}
	for _, k := range otp.Sorted(compareKeys) {
		rows, _ := otp.Get(k)
		codes, all := 0, true
		for _, r := range rows {
			if r.OTP6 || r.VCode {
				codes++
			}
			all = all && claimed[r.ID]
		}
		if len(rows) >= p.minGroup && float64(codes)/float64(len(rows)) >= p.otpMinShare && !all {
			found = append(found, candidate{ReasonOTP, levels[1], k.account, k.key, k.tmpl, rows, codes})
		}
	}
	return found
}

func compareKeys(a, b groupKey) int {
	switch {
	case a.account != b.account:
		return strings.Compare(a.account, b.account)
	case a.key != b.key:
		return strings.Compare(a.key, b.key)
	}
	return strings.Compare(a.tmpl, b.tmpl)
}

func buildRule(account string, lv level, key, tmpl string, olderThanDays int) map[string]any {
	when := []any{
		map[string]any{"field": lv.column, "op": lv.op, "value": key},
		map[string]any{"field": "subject_tmpl", "op": "eq", "value": tmpl},
	}
	reason := ReasonDeleted
	if olderThanDays != 0 {
		when = append(when, map[string]any{"field": rules.AgeField, "op": rules.AgeOp, "value": olderThanDays})
		reason = ReasonOTP
	}
	return map[string]any{
		"id":      "del-" + scanutil.Digest(account, reason, lv.name, key, tmpl)[:10],
		"account": account, "action": "delete", "when": when,
	}
}

func (p params) build(ctx context.Context, q proposals.Querier, everyone []*row, existing []rules.Rule, c candidate, now float64) (*scanutil.Draft, error) {
	otp := c.Reason == ReasonOTP
	days := 0
	if otp {
		days = p.otpOlderThanDays
	}
	rule := buildRule(c.Account, c.Level, c.Key, c.Tmpl, days)
	parsed, err := rules.ValidateRule(rule)
	if err != nil {
		return nil, err
	}
	for _, r := range existing {
		if r.ID == parsed.ID {
			return nil, nil
		}
	}
	if match, err := rules.FirstMatch(&c.Rows[0].Message, existing, now); err != nil || match != nil {
		return nil, err
	}
	var backlog []*row
	for _, r := range everyone {
		if r.Gone || r.Account != c.Account || r.Flagged || !otp && !r.Seen {
			continue
		}
		ok, err := parsed.Matches(&r.Message, now)
		if err != nil {
			return nil, err
		}
		if ok {
			backlog = append(backlog, r)
		}
	}
	ids := make([]int64, len(backlog))
	views := make([]any, len(backlog))
	messages := make([]scanutil.Message, len(backlog))
	senders := make([]string, len(backlog))
	for i, r := range backlog {
		ids[i] = r.ID
		views[i] = r.view()
		messages[i] = scanutil.Message{ID: r.ID, Subject: r.Subject, FromName: r.FromName, FromAddr: r.FromAddr, DateTS: r.DateTS}
		senders[i] = r.FromAddr
	}
	snaps, err := mailops.Snapshots(ctx, q, ids)
	if err != nil {
		return nil, err
	}
	when, ok := rule["when"]
	if !ok {
		return nil, fmt.Errorf("%w: rule has no conditions", httpx.ErrShape)
	}
	return &scanutil.Draft{
		Title:   fmt.Sprintf("Delete %d messages and future mail matching %s / %s", len(ids), c.Key, c.Tmpl),
		Payload: map[string]any{"rule": rule, "reason": c.Reason, "messages": scanutil.SnapshotList(snaps)},
		Preview: map[string]any{"reason": c.Reason, "rule": map[string]any{"account": c.Account, "when": when}, "backlog": len(ids), "subjects": p.limits.SampleSubjectsOf(messages), "senders": p.limits.TopCounts(senders), "messages": views},
	}, nil
}

func Scan(ctx context.Context, d *db.DB, ts float64) (scanutil.Result, error) {
	p, err := readParams(ctx, d.Read)
	if err != nil {
		return scanutil.Result{}, err
	}
	accounts, err := config.ExistingAccounts(ctx, d.Read)
	if err != nil {
		return scanutil.Result{}, err
	}
	names := make([]string, len(accounts))
	loaded := map[string][]rules.Rule{}
	for i, spec := range accounts {
		names[i] = spec.Name
		if loaded[spec.Name], err = rules.LoadRules(ctx, d.Read, spec.Name); err != nil {
			return scanutil.Result{}, err
		}
	}
	everyone, err := received(ctx, d.Read, names)
	if err != nil {
		return scanutil.Result{}, err
	}
	deleted, err := deletedIDs(ctx, d.Read)
	if err != nil {
		return scanutil.Result{}, err
	}
	found := p.candidates(everyone, deleted)
	created := 0
	for _, c := range found {
		fingerprint := fmt.Sprintf("%s:%s:%s:%s:%s", Kind, c.Account, c.Reason, c.Level.name, scanutil.Digest(c.Key, c.Tmpl))
		account := c.Account
		_, ok, err := scanutil.Publish(ctx, d, ts, Kind, &account, fingerprint, func() (*scanutil.Draft, error) {
			return p.build(ctx, d.Read, everyone, loaded[c.Account], c, ts)
		})
		if err != nil {
			return scanutil.Result{}, err
		}
		if ok {
			created++
		}
	}
	log.Info(fmt.Sprintf("deletions: %d candidate groups, %d new proposals", len(found), created))
	return scanutil.Result{Candidates: len(found), Created: created}, nil
}

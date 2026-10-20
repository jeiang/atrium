package deletions

import (
	"context"
	"database/sql"
	"fmt"
	"slices"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/mail/mailops"
	"github.com/jeiang/atrium/internal/mail/rules"
	"github.com/jeiang/atrium/internal/proposals"
	"github.com/jeiang/atrium/internal/proposals/scanutil"
	"github.com/jeiang/atrium/internal/py"
)

var log = logx.Logger("atrium.deletions")

const (
	Kind             = "deletion_rule"
	MinGroup         = 5
	MinDeletedShare  = 0.7
	OTPMinShare      = 0.8
	OTPOlderThanDays = 7
	ReasonDeleted    = "operator_deleted"
	ReasonOTP        = "otp"
)

type level struct {
	name, column, op string
}

var levels = []level{{"addr", "from_addr", "eq"}, {"domain", "from_domain", "suffix"}}

type row struct {
	rules.Message
	Seen, Flagged, OTP6, VCode, Gone bool
}

func (r *row) column(name string) string {
	if name == "from_addr" {
		return r.FromAddr
	}
	return r.FromDomain
}

func (r *row) view() py.Dict {
	return scanutil.Message{ID: r.ID, Subject: r.Subject, FromName: r.FromName, FromAddr: r.FromAddr, DateTS: r.DateTS}.View()
}

type withExtra struct {
	s     rules.Scanner
	extra []any
}

func (w withExtra) Scan(dest ...any) error { return w.s.Scan(append(dest, w.extra...)...) }

func received(ctx context.Context, q proposals.Querier) ([]*row, error) {
	rows, err := q.QueryContext(ctx, "SELECT "+rules.MessageColumns+", seen, flagged, k_otp6, k_vcode, gone_at IS NOT NULL "+
		"FROM messages WHERE sent = 0 ORDER BY id")
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (*row, error) {
		var m row
		var err error
		m.Message, err = rules.ScanMessage(withExtra{r, []any{&m.Seen, &m.Flagged, &m.OTP6, &m.VCode, &m.Gone}})
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

type Candidate struct {
	Reason  string
	Level   level
	Account string
	Key     string
	Tmpl    string
	Rows    []*row
	Hits    int
}

type groupKey struct{ account, key, tmpl string }

type groups struct {
	order []groupKey
	rows  map[groupKey][]*row
}

func (g *groups) add(k groupKey, r *row) {
	if g.rows == nil {
		g.rows = map[groupKey][]*row{}
	}
	if _, seen := g.rows[k]; !seen {
		g.order = append(g.order, k)
	}
	g.rows[k] = append(g.rows[k], r)
}

func (g *groups) sorted() []groupKey {
	keys := slices.Clone(g.order)
	slices.SortFunc(keys, func(a, b groupKey) int {
		switch {
		case a.account != b.account:
			return compare(a.account, b.account)
		case a.key != b.key:
			return compare(a.key, b.key)
		}
		return compare(a.tmpl, b.tmpl)
	})
	return keys
}

func compare(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func candidates(everyone []*row, deleted map[int64]bool) []Candidate {
	var found []Candidate
	claimed := map[int64]bool{}
	for _, lv := range levels {
		var g groups
		for _, r := range everyone {
			if !claimed[r.ID] && r.column(lv.column) != "" && r.SubjectTmpl != "" {
				g.add(groupKey{r.Account, r.column(lv.column), r.SubjectTmpl}, r)
			}
		}
		for _, k := range g.sorted() {
			rows := g.rows[k]
			gone := 0
			for _, r := range rows {
				if deleted[r.ID] {
					gone++
				}
			}
			if len(rows) >= MinGroup && float64(gone)/float64(len(rows)) >= MinDeletedShare {
				for _, r := range rows {
					claimed[r.ID] = true
				}
				found = append(found, Candidate{ReasonDeleted, lv, k.account, k.key, k.tmpl, rows, gone})
			}
		}
	}
	var otp groups
	for _, r := range everyone {
		if r.FromDomain != "" && r.SubjectTmpl != "" {
			otp.add(groupKey{r.Account, r.FromDomain, r.SubjectTmpl}, r)
		}
	}
	for _, k := range otp.sorted() {
		rows := otp.rows[k]
		codes, all := 0, true
		for _, r := range rows {
			if r.OTP6 || r.VCode {
				codes++
			}
			all = all && claimed[r.ID]
		}
		if len(rows) >= MinGroup && float64(codes)/float64(len(rows)) >= OTPMinShare && !all {
			found = append(found, Candidate{ReasonOTP, levels[1], k.account, k.key, k.tmpl, rows, codes})
		}
	}
	return found
}

func BuildRule(account, levelName, key, tmpl string, olderThanDays int) py.Dict {
	lv := levels[0]
	for _, l := range levels {
		if l.name == levelName {
			lv = l
		}
	}
	when := []any{
		py.Dict{{Key: "field", Value: lv.column}, {Key: "op", Value: lv.op}, {Key: "value", Value: key}},
		py.Dict{{Key: "field", Value: "subject_tmpl"}, {Key: "op", Value: "eq"}, {Key: "value", Value: tmpl}},
	}
	reason := ReasonDeleted
	if olderThanDays != 0 {
		when = append(when, py.Dict{{Key: "field", Value: rules.AgeField}, {Key: "op", Value: rules.AgeOp}, {Key: "value", Value: olderThanDays}})
		reason = ReasonOTP
	}
	return py.Dict{
		{Key: "id", Value: "del-" + scanutil.Digest(account, reason, levelName, key, tmpl)[:10]},
		{Key: "account", Value: account}, {Key: "action", Value: "delete"}, {Key: "when", Value: when},
	}
}

type proposal struct {
	fingerprint      string
	title            string
	payload, preview py.Dict
}

func build(ctx context.Context, q proposals.Querier, everyone []*row, c Candidate, now float64) (*proposal, error) {
	otp := c.Reason == ReasonOTP
	days := 0
	if otp {
		days = OTPOlderThanDays
	}
	rule := BuildRule(c.Account, c.Level.name, c.Key, c.Tmpl, days)
	parsed, err := rules.ValidateRule(rule)
	if err != nil {
		return nil, err
	}
	existing, err := rules.LoadRules(ctx, q, c.Account)
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
	var older any
	if otp {
		older = OTPOlderThanDays
	}
	when, _ := rule.Get("when")
	return &proposal{
		fingerprint: fmt.Sprintf("%s:%s:%s:%s:%s", Kind, c.Account, c.Reason, c.Level.name, scanutil.Digest(c.Key, c.Tmpl)),
		title:       fmt.Sprintf("Delete %d messages and future mail matching %s / %s", len(ids), c.Key, c.Tmpl),
		payload: py.Dict{
			{Key: "rule", Value: rule}, {Key: "reason", Value: c.Reason}, {Key: "messages", Value: scanutil.SnapshotList(snaps)},
		},
		preview: py.Dict{
			{Key: "reason", Value: c.Reason},
			{Key: "rule", Value: py.Dict{{Key: "account", Value: c.Account}, {Key: "when", Value: when}}},
			{Key: "stats", Value: py.Dict{{Key: "messages", Value: len(c.Rows)}, {Key: "matched", Value: c.Hits}}},
			{Key: "older_than_days", Value: older}, {Key: "backlog", Value: len(ids)},
			{Key: "sample", Value: views[:min(len(views), 10)]}, {Key: "subjects", Value: scanutil.SampleSubjectsOf(messages)},
			{Key: "senders", Value: scanutil.TopCounts(senders)}, {Key: "messages", Value: views},
		},
	}, nil
}

func Scan(ctx context.Context, d *db.DB, ts float64) (scanutil.Result, error) {
	everyone, err := received(ctx, d.Read)
	if err != nil {
		return scanutil.Result{}, err
	}
	deleted, err := deletedIDs(ctx, d.Read)
	if err != nil {
		return scanutil.Result{}, err
	}
	found := candidates(everyone, deleted)
	created := 0
	for _, c := range found {
		b, err := build(ctx, d.Read, everyone, c, ts)
		if err != nil {
			return scanutil.Result{}, err
		}
		if b == nil {
			continue
		}
		known, err := proposals.Known(ctx, d.Write, b.fingerprint)
		if err != nil {
			return scanutil.Result{}, err
		}
		if known {
			continue
		}
		account := c.Account
		_, ok, err := proposals.Create(ctx, d, Kind, &account, b.title, b.payload, b.preview, b.fingerprint, ts)
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

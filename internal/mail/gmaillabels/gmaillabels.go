package gmaillabels

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/mailops"
	"github.com/jeiang/atrium/internal/proposals"
	"github.com/jeiang/atrium/internal/proposals/scanutil"
	"github.com/jeiang/atrium/internal/py"
)

var log = logx.Logger("atrium.gmail_labels")

const (
	Kind      = "gmail_label_merge"
	Hierarchy = "/"
)

type mailbox struct {
	members     map[string][]int64
	rows        map[int64]scanutil.Message
	maxMessages int
	minTarget   int
	minOverlap  float64
}

func (m mailbox) labels() []string {
	out := make([]string, 0, len(m.members))
	for label := range m.members {
		out = append(out, label)
	}
	slices.Sort(out)
	return out
}

func load(ctx context.Context, q proposals.Querier) (mailbox, error) {
	m := mailbox{members: map[string][]int64{}, rows: map[int64]scanutil.Message{}}
	r := mail.Read(ctx, q)
	m.maxMessages = r.Int(mail.KeyGmailLabelsMaxMessages)
	m.minTarget = r.Int(mail.KeyGmailLabelsMinTarget)
	m.minOverlap = r.Float(mail.KeyGmailLabelsMinOverlap)
	if err := r.Err(); err != nil {
		return m, err
	}
	rows, err := q.QueryContext(ctx, "SELECT id, labels, subject, from_name, from_addr, date_ts FROM messages "+
		"WHERE account = ? AND gone_at IS NULL AND sent = 0", config.Gmail)
	if err != nil {
		return m, err
	}
	type labelled struct {
		scanutil.Message
		labels string
	}
	all, err := db.Collect(rows, func(r *sql.Rows) (labelled, error) {
		var l labelled
		err := r.Scan(&l.ID, &l.labels, &l.Subject, &l.FromName, &l.FromAddr, &l.DateTS)
		return l, err
	})
	if err != nil {
		return m, err
	}
	for _, l := range all {
		var labels []string
		if err := json.Unmarshal([]byte(l.labels), &labels); err != nil {
			return m, err
		}
		m.rows[l.ID] = l.Message
		for _, label := range labels {
			if !strings.HasPrefix(label, `\`) && !strings.HasPrefix(label, "[") {
				m.members[label] = append(m.members[label], l.ID)
			}
		}
	}
	return m, nil
}

func distinct(ids []int64) []int64 {
	return slices.Compact(slices.Sorted(slices.Values(ids)))
}

func parentOf(label string) (string, bool) {
	i := strings.LastIndex(label, Hierarchy)
	if i < 0 {
		return "", false
	}
	return label[:i], true
}

func (m mailbox) bestSibling(label string) string {
	senders := map[string]int{}
	total := 0
	for _, id := range distinct(m.members[label]) {
		if addr := m.rows[id].FromAddr; addr != "" {
			senders[addr]++
			total++
		}
	}
	if total == 0 {
		return ""
	}
	best, bestShare := "", 0.0
	for _, other := range m.labels() {
		ids := m.members[other]
		if _, nested := parentOf(other); other == label || nested || len(ids) < m.minTarget {
			continue
		}
		theirs := map[string]bool{}
		for _, id := range ids {
			theirs[m.rows[id].FromAddr] = true
		}
		shared := 0
		for addr, n := range senders {
			if theirs[addr] {
				shared += n
			}
		}
		if share := float64(shared) / float64(total); share > bestShare {
			best, bestShare = other, share
		}
	}
	if bestShare >= m.minOverlap {
		return best
	}
	return ""
}

type candidate struct {
	label, into string
	ids         []int64
}

func (m mailbox) candidates() []candidate {
	var found []candidate
	labels := m.labels()
	for _, label := range labels {
		ids := m.members[label]
		if len(ids) >= m.maxMessages || slices.ContainsFunc(labels, func(o string) bool { return strings.HasPrefix(o, label+Hierarchy) }) {
			continue
		}
		into, _ := parentOf(label)
		if into == "" {
			into = m.bestSibling(label)
		}
		if into != "" {
			found = append(found, candidate{label, into, ids})
		}
	}
	return found
}

func Scan(ctx context.Context, d *db.DB, ts float64) (scanutil.Result, error) {
	m, err := load(ctx, d.Read)
	if err != nil {
		return scanutil.Result{}, err
	}
	found := m.candidates()
	created := 0
	for _, c := range found {
		fingerprint := Kind + ":" + scanutil.Digest(c.label, c.into)
		known, err := proposals.Known(ctx, d.Write, fingerprint)
		if err != nil {
			return scanutil.Result{}, err
		}
		if known {
			continue
		}
		snaps, err := mailops.Snapshots(ctx, d.Read, c.ids)
		if err != nil {
			return scanutil.Result{}, err
		}
		ids := distinct(c.ids)
		views := make([]any, len(ids))
		for i, id := range ids {
			views[i] = m.rows[id].View()
		}
		account := config.Gmail
		_, ok, err := proposals.Create(ctx, d, Kind, &account, fmt.Sprintf("Merge label %s into %s", c.label, c.into),
			py.Dict{{Key: "label", Value: c.label}, {Key: "into", Value: c.into}, {Key: "messages", Value: scanutil.SnapshotList(snaps)}},
			py.Dict{
				{Key: "from", Value: c.label}, {Key: "into", Value: c.into}, {Key: "from_count", Value: len(c.ids)},
				{Key: "into_count", Value: len(m.members[c.into])}, {Key: "messages", Value: views},
			}, fingerprint, ts)
		if err != nil {
			return scanutil.Result{}, err
		}
		if ok {
			created++
		}
	}
	log.Info(fmt.Sprintf("gmail labels: %d candidates, %d new proposals", len(found), created))
	return scanutil.Result{Candidates: len(found), Created: created}, nil
}

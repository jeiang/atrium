// Package backlog is backlog.py: the read Inbox mail that was already eligible at go-live becomes one
// inbox_backlog proposal per account, since it is never filed automatically.
package backlog

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/mail/live"
	"github.com/jeiang/atrium/internal/mail/mailops"
	"github.com/jeiang/atrium/internal/proposals"
	"github.com/jeiang/atrium/internal/proposals/scanutil"
	"github.com/jeiang/atrium/internal/py"
)

var log = logx.Logger("atrium.backlog")

const Kind = "inbox_backlog"

const candidates = "SELECT m.id, m.subject, m.from_name, m.from_addr, m.date_ts, d.dest " +
	"FROM messages m JOIN mailboxes mb ON mb.id = m.mailbox_id JOIN decisions d ON d.id = (SELECT MAX(x.id) FROM decisions x WHERE x.message_id = m.id) " +
	"WHERE m.account = :account AND m.in_inbox = 1 AND m.seen = 1 AND m.flagged = 0 AND m.sent = 0 " +
	"AND m.gone_at IS NULL AND d.action = 'file' AND d.dest IS NOT NULL " +
	"AND m.id IN (SELECT message_id FROM live_baseline) " +
	"AND " + live.NoNewCorrection + " AND " + live.NotBlocked + " AND NOT " + mailops.UnresolvedSQL + " ORDER BY m.id"

// Error is BacklogError.
type Error string

func (e Error) Error() string { return string(e) }

func (Error) PyClass() string { return "BacklogError" }

func (Error) PyModule() string { return "atrium.backlog" }

// Proposal is what build returns: the inbox_backlog proposal for one account.
type Proposal struct {
	Fingerprint string
	Account     string
	Title       string
	Payload     py.Dict
	Preview     py.Dict
}

type group struct {
	dest string
	rows []scanutil.Message
}

// Build is build: nil when the account has no backlog.
func Build(ctx context.Context, q live.Querier, account string) (*Proposal, error) {
	params, err := live.QueryParams(ctx, q, account)
	if err != nil {
		return nil, err
	}
	rows, err := q.QueryContext(ctx, candidates, params.Args()...)
	if err != nil {
		return nil, err
	}
	var groups []*group
	byDest := map[string]*group{}
	var ids []any
	for rows.Next() {
		var m scanutil.Message
		var dest string
		if err := rows.Scan(&m.ID, &m.Subject, &m.FromName, &m.FromAddr, &m.DateTS, &dest); err != nil {
			_ = rows.Close()
			return nil, err
		}
		g := byDest[dest]
		if g == nil {
			g = &group{dest: dest}
			byDest[dest] = g
			groups = append(groups, g)
		}
		g.rows = append(g.rows, m)
		ids = append(ids, m.ID)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, nil
	}
	slices.SortStableFunc(groups, func(a, b *group) int {
		return cmp.Or(cmp.Compare(len(b.rows), len(a.rows)), cmp.Compare(a.dest, b.dest))
	})
	golive, err := live.GetState(ctx, q, live.GoliveKey)
	if err != nil {
		return nil, err
	}
	var goliveText any = "None"
	if golive != nil {
		goliveText = *golive
	}
	gmail := account == config.Gmail
	folder, unit := config.Inbox, "folders"
	if gmail {
		folder, unit = mailops.InboxLabel, "labels"
	}
	payloadGroups := make([]any, len(groups))
	previewGroups := make([]any, len(groups))
	for i, g := range groups {
		groupIDs := make([]int64, len(g.rows))
		views := make([]any, len(g.rows))
		senders := make([]string, len(g.rows))
		for j, r := range g.rows {
			groupIDs[j], views[j], senders[j] = r.ID, r.View(), r.FromAddr
		}
		snaps, err := mailops.Snapshots(ctx, q, groupIDs)
		if err != nil {
			return nil, err
		}
		messages := make([]any, len(snaps))
		for j, s := range snaps {
			messages[j] = s.Dict()
		}
		payloadGroups[i] = py.Dict{{Key: "dest", Value: g.dest}, {Key: "messages", Value: messages}}
		previewGroups[i] = py.Dict{
			{Key: "name", Value: g.dest}, {Key: "count", Value: len(g.rows)}, {Key: "terms", Value: []any{}},
			{Key: "senders", Value: scanutil.TopCounts(senders)}, {Key: "subjects", Value: scanutil.SampleSubjectsOf(g.rows)},
			{Key: "messages", Value: views},
		}
	}
	return &Proposal{
		Fingerprint: fmt.Sprintf("%s:%s:%s", Kind, account, scanutil.Digest(append([]any{goliveText}, ids...)...)),
		Account:     account,
		Title:       fmt.Sprintf("File %d read inbox messages into %d %s", len(ids), len(groups), unit),
		Payload:     py.Dict{{Key: "folder", Value: folder}, {Key: "groups", Value: payloadGroups}},
		Preview:     py.Dict{{Key: "folder", Value: folder}, {Key: "groups", Value: previewGroups}},
	}, nil
}

// Result is backlog's outcome: the ids created and, per known account, its proposal id or nil.
type Result struct {
	Created  []int64 `json:"created"`
	Accounts py.Dict `json:"accounts"`
}

// Backlog is backlog: one proposal per account in the accounts table, created at ts, the caller's clock (Python's
// now()). It fails before go-live.
func Backlog(ctx context.Context, d *db.DB, ts float64) (Result, error) {
	out := Result{Created: []int64{}, Accounts: py.Dict{}}
	golive, err := live.GetState(ctx, d.Write, live.GoliveKey)
	if err != nil {
		return out, err
	}
	if golive == nil {
		return out, Error("the live service has not run yet: no go-live baseline exists")
	}
	for _, spec := range config.Accounts {
		var one int
		err := d.Write.QueryRowContext(ctx, "SELECT 1 FROM accounts WHERE name = ?", spec.Name).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return out, err
		}
		built, err := Build(ctx, d.Write, spec.Name)
		if err != nil {
			return out, err
		}
		if built == nil {
			out.Accounts = append(out.Accounts, py.Pair{Key: spec.Name})
			continue
		}
		known, err := proposals.Known(ctx, d.Write, built.Fingerprint)
		if err != nil {
			return out, err
		}
		if known {
			out.Accounts = append(out.Accounts, py.Pair{Key: spec.Name})
			continue
		}
		id, ok, err := proposals.Create(ctx, d, Kind, new(spec.Name), built.Title, built.Payload, built.Preview, built.Fingerprint, ts)
		if err != nil {
			return out, err
		}
		if !ok {
			out.Accounts = append(out.Accounts, py.Pair{Key: spec.Name})
			continue
		}
		out.Accounts = append(out.Accounts, py.Pair{Key: spec.Name, Value: id})
		out.Created = append(out.Created, id)
	}
	log.Info("inbox backlog proposals: " + py.ReprValue(out.Accounts))
	return out, nil
}

// Refresh is refresh: the pending backlog proposals are dismissed at ts and the backlog is built again with the
// same ts; the caller passes its clock once, where Python read now() for each.
func Refresh(ctx context.Context, d *db.DB, ts float64) (Result, error) {
	if _, err := d.Write.ExecContext(ctx, "UPDATE proposals SET status = 'dismissed', decided_ts = ? WHERE kind = ? AND status = 'pending'",
		ts, Kind); err != nil {
		return Result{}, err
	}
	return Backlog(ctx, d, ts)
}

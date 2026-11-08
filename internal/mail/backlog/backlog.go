package backlog

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/mail/live"
	"github.com/jeiang/atrium/internal/mail/mailops"
	"github.com/jeiang/atrium/internal/proposals/scanutil"
)

var log = logx.Logger("atrium.backlog")

const Kind = "inbox_backlog"

var candidates = "SELECT m.id, m.subject, m.from_name, m.from_addr, m.date_ts, d.dest " +
	"FROM messages m JOIN mailboxes mb ON mb.id = m.mailbox_id JOIN decisions d ON d.id = (SELECT MAX(x.id) FROM decisions x WHERE x.message_id = m.id) " +
	"WHERE m.account = :account AND m.in_inbox = 1 AND m.seen = 1 AND m.flagged = 0 AND m.sent = 0 " +
	"AND m.gone_at IS NULL AND d.action = 'file' AND d.dest IS NOT NULL " +
	"AND m.id IN (SELECT message_id FROM live_baseline) " +
	"AND " + live.NoNewCorrection + " AND " + live.NotBlocked + " AND NOT " + ledger.MailUnresolvedSQL + " ORDER BY m.id"

type Error string

func (e Error) Error() string { return string(e) }

type group struct {
	dest string
	rows []scanutil.Message
}

type detection struct {
	account    string
	ids        []string
	groups     []*group
	goliveText string
	folder     string
	unit       string
}

func detect(ctx context.Context, q live.Querier, account string) (*detection, error) {
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
	var ids []string
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
		ids = append(ids, strconv.FormatInt(m.ID, 10))
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
	goliveText := "None"
	if golive != nil {
		goliveText = *golive
	}
	gmail := config.From(ctx).Preset(account) == config.Gmail
	folder, unit := config.Inbox, "folders"
	if gmail {
		folder, unit = mailops.InboxLabel, "labels"
	}
	return &detection{account: account, ids: ids, groups: groups, goliveText: goliveText, folder: folder, unit: unit}, nil
}

func (d *detection) fingerprint() string {
	return fmt.Sprintf("%s:%s:%s", Kind, d.account, scanutil.Digest(append([]string{d.goliveText}, d.ids...)...))
}

func (d *detection) draft(ctx context.Context, q live.Querier) (*scanutil.Draft, error) {
	payloadGroups := make([]any, len(d.groups))
	previewGroups := make([]any, len(d.groups))
	lim, err := scanutil.ReadLimits(ctx, q)
	if err != nil {
		return nil, err
	}
	for i, g := range d.groups {
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
		payloadGroups[i] = map[string]any{"dest": g.dest, "messages": scanutil.SnapshotList(snaps)}
		previewGroups[i] = map[string]any{"name": g.dest, "count": len(g.rows), "senders": lim.TopCounts(senders), "subjects": lim.SampleSubjectsOf(g.rows), "messages": views}
	}
	return &scanutil.Draft{
		Title:   fmt.Sprintf("File %d read inbox messages into %d %s", len(d.ids), len(d.groups), d.unit),
		Payload: map[string]any{"folder": d.folder, "groups": payloadGroups},
		Preview: map[string]any{"folder": d.folder, "groups": previewGroups},
	}, nil
}

type Accounts struct {
	names  []string
	values map[string]any
}

func (a *Accounts) Set(name string, value any) {
	if a.values == nil {
		a.values = map[string]any{}
	}
	if _, seen := a.values[name]; !seen {
		a.names = append(a.names, name)
	}
	a.values[name] = value
}

func (a Accounts) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, name := range a.names {
		if i > 0 {
			b.WriteByte(',')
		}
		key, err := json.Marshal(name)
		if err != nil {
			return nil, err
		}
		value, err := json.Marshal(a.values[name])
		if err != nil {
			return nil, err
		}
		b.Write(key)
		b.WriteByte(':')
		b.Write(value)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

type Result struct {
	Created  []int64  `json:"created"`
	Accounts Accounts `json:"accounts"`
}

func Backlog(ctx context.Context, d *db.DB, ts float64) (Result, error) {
	out := Result{Created: []int64{}}
	golive, err := live.GetState(ctx, d.Write, live.GoliveKey)
	if err != nil {
		return out, err
	}
	if golive == nil {
		return out, Error("the live service has not run yet: no go-live baseline exists")
	}
	accounts, err := config.ExistingAccounts(ctx, d.Read)
	if err != nil {
		return out, err
	}
	for _, spec := range accounts {
		detected, err := detect(ctx, d.Read, spec.Name)
		if err != nil {
			return out, err
		}
		if detected == nil {
			out.Accounts.Set(spec.Name, nil)
			continue
		}
		account := spec.Name
		id, created, err := scanutil.Publish(ctx, d, ts, Kind, &account, detected.fingerprint(), func() (*scanutil.Draft, error) {
			return detected.draft(ctx, d.Read)
		})
		if err != nil {
			return out, err
		}
		if !created {
			out.Accounts.Set(spec.Name, nil)
			continue
		}
		out.Accounts.Set(spec.Name, id)
		out.Created = append(out.Created, id)
	}
	log.Info("inbox backlog proposals: " + fmt.Sprint(out.Accounts.values))
	return out, nil
}

func Refresh(ctx context.Context, d *db.DB, ts float64) (Result, error) {
	if _, err := d.Write.ExecContext(ctx, "UPDATE proposals SET status = 'dismissed', decided_ts = ? WHERE kind = ? AND status = 'pending'",
		ts, Kind); err != nil {
		return Result{}, err
	}
	return Backlog(ctx, d, ts)
}

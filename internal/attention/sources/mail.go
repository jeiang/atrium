package sources

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"time"

	"github.com/jeiang/atrium/internal/attention"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/mail/birthdays"
	"github.com/jeiang/atrium/internal/mail/drafts"
	"github.com/jeiang/atrium/internal/mail/openloops"
	"github.com/jeiang/atrium/internal/mail/watches"
)

func Mail(env config.Env) []attention.Source {
	return []attention.Source{
		attention.SourceFunc(openDrafts),
		attention.SourceFunc(func(ctx context.Context, q db.Querier, now time.Time, _ *time.Location) ([]attention.Item, error) {
			return loops(ctx, q, now, openloops.OweReply)
		}),
		attention.SourceFunc(func(ctx context.Context, q db.Querier, now time.Time, _ *time.Location) ([]attention.Item, error) {
			return loops(ctx, q, now, openloops.Waiting)
		}),
		attention.SourceFunc(myCommitments),
		attention.SourceFunc(func(ctx context.Context, q db.Querier, now time.Time, _ *time.Location) ([]attention.Item, error) {
			return packages(ctx, q, env, now)
		}),
		attention.SourceFunc(upcomingBirthdays),
	}
}

func openDrafts(ctx context.Context, q db.Querier, _ time.Time, _ *time.Location) ([]attention.Item, error) {
	rows, err := q.QueryContext(ctx, "SELECT id, to_addrs, subject, created_at FROM drafts WHERE status = ? ORDER BY created_at, id LIMIT ?",
		drafts.Draft, attention.QueueLimit)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (attention.Item, error) {
		var id int64
		var to, subject string
		it := attention.Item{Kind: attention.KindDraft, Class: attention.NeedsDecision}
		err := r.Scan(&id, &to, &subject, &it.TS)
		it.SourceID = strconv.FormatInt(id, 10)
		it.Title = to + ": " + subject
		it.Actions = []attention.Action{
			{Label: "Open", Method: "GET", Path: "/drafts/" + it.SourceID},
			{Label: "Discard", Method: "POST", Path: fmt.Sprintf("/drafts/%s/discard", it.SourceID)},
		}
		return it, err
	})
}

func loops(ctx context.Context, q db.Querier, now time.Time, state string) ([]attention.Item, error) {
	list, err := openloops.ListLoops(ctx, q, attention.Seconds(now), state, attention.QueueLimit, false)
	if err != nil {
		return nil, err
	}
	out := make([]attention.Item, 0, len(list))
	for _, l := range list {
		id := strconv.FormatInt(l.ID, 10)
		it := attention.Item{Kind: attention.KindOweReply, SourceID: id, Title: l.Counterpart + ": " + l.Subject, Class: attention.NeedsDecision, TS: l.Since}
		if state == openloops.Waiting {
			it.Kind, it.Class, it.Group = attention.KindWaiting, attention.Info, attention.GroupWaiting
		} else {
			it.Actions = []attention.Action{
				{Label: "Snooze", Method: "POST", Path: fmt.Sprintf("/loops/%s/snooze", id)},
				{Label: "Dismiss", Method: "POST", Path: fmt.Sprintf("/loops/%s/dismiss", id)},
			}
		}
		out = append(out, it)
	}
	return out, nil
}

func myCommitments(ctx context.Context, q db.Querier, now time.Time, loc *time.Location) ([]attention.Item, error) {
	rows, err := q.QueryContext(ctx,
		"SELECT id, text, COALESCE(due, ''), created_ts FROM commitments WHERE status = ? AND owner = 'me' ORDER BY id LIMIT ?",
		openloops.Open, attention.QueueLimit)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (attention.Item, error) {
		var id int64
		it := attention.Item{Kind: attention.KindCommitment}
		err := r.Scan(&id, &it.Title, &it.Due, &it.TS)
		it.SourceID = strconv.FormatInt(id, 10)
		it.Class = attention.DueClass(it.Due, now, loc)
		it.Actions = []attention.Action{
			{Label: "Done", Method: "POST", Path: fmt.Sprintf("/loops/commitments/%s/done", it.SourceID)},
			{Label: "Dismiss", Method: "POST", Path: fmt.Sprintf("/loops/commitments/%s/dismiss", it.SourceID)},
		}
		return it, err
	})
}

func packages(ctx context.Context, q db.Querier, env config.Env, now time.Time) ([]attention.Item, error) {
	list, err := watches.ListPackages(ctx, q, env, attention.Seconds(now), true, false)
	if err != nil {
		return nil, err
	}
	out := make([]attention.Item, 0, len(list))
	for _, p := range list {
		out = append(out, attention.Item{
			Kind: attention.KindPackage, SourceID: strconv.FormatInt(p.ID, 10), Title: p.Merchant + ": " + p.StatusText,
			Class: attention.Info, TS: p.UpdatedTS, Group: attention.GroupUpcoming,
		})
	}
	return out, nil
}

func upcomingBirthdays(ctx context.Context, q db.Querier, now time.Time, loc *time.Location) ([]attention.Item, error) {
	y, m, d := now.In(loc).Date()
	list, _, err := birthdays.UpcomingFor(ctx, q, birthdays.Date(y, m, d), birthdays.WindowDays)
	if err != nil {
		return nil, err
	}
	out := make([]attention.Item, 0, len(list))
	for _, b := range list {
		due, err := time.ParseInLocation("2006-01-02", b.Date, loc)
		if err != nil {
			return nil, err
		}
		out = append(out, attention.Item{
			Kind: attention.KindBirthday, SourceID: b.Key, Title: b.Name + ": " + b.Label, Class: attention.Info,
			Due: b.Date, TS: attention.Seconds(due), Group: attention.GroupUpcoming,
		})
	}
	return out, nil
}

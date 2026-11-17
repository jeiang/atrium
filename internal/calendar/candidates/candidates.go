package candidates

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/apps/calendar"
	"github.com/jeiang/atrium/internal/communications"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/module"
)

var log = logx.Logger("atrium.event_candidates")

type Status string

const (
	StatusQueued   Status = "queued"
	StatusDrafted  Status = "drafted"
	StatusReviewed Status = "reviewed"
	StatusProposed Status = "proposed"
	StatusNone     Status = "none"
)

const invitationCap = 16 << 20

var calendarTypes = map[string]bool{"text/calendar": true, "application/ics": true}

func set(ctx context.Context, d *db.DB, messageID int64, status Status, reason string, now float64) error {
	_, err := d.Write.ExecContext(ctx, "INSERT OR REPLACE INTO event_candidates (message_id, reason, status, ts) VALUES (?, ?, ?, ?)",
		messageID, reason, string(status), now)
	return err
}

func Drafted(ctx context.Context, d *db.DB, messageID int64, now float64) (bool, error) {
	res, err := d.Write.ExecContext(ctx,
		"UPDATE event_candidates SET status = ?, ts = ? WHERE message_id = ? AND status = ?",
		string(StatusDrafted), now, messageID, string(StatusQueued))
	if err != nil {
		return false, err
	}
	changed, err := res.RowsAffected()
	return changed > 0, err
}

type Item struct {
	MessageID int64  `json:"message_id"`
	Account   string `json:"account"`
	Reason    string `json:"reason"`
}

func Queued(ctx context.Context, q db.Querier, limit int) ([]Item, error) {
	rows, err := q.QueryContext(ctx, "SELECT message_id, reason FROM event_candidates WHERE status = ? ORDER BY ts, message_id",
		string(StatusQueued))
	if err != nil {
		return nil, err
	}
	found, err := db.Collect(rows, func(r *sql.Rows) (Item, error) {
		var item Item
		return item, r.Scan(&item.MessageID, &item.Reason)
	})
	items := []Item{}
	if err != nil || len(found) == 0 {
		return items, err
	}
	ids := make([]int64, len(found))
	for i, item := range found {
		ids[i] = item.MessageID
	}
	messages, err := module.Communications(ctx).Messages(ctx, q, communications.Query{IDs: ids})
	if err != nil {
		return items, err
	}
	accounts := make(map[int64]string, len(messages))
	for _, m := range messages {
		accounts[m.ID] = m.Account
	}
	for _, item := range found {
		if limit >= 0 && len(items) >= limit {
			break
		}
		account, ok := accounts[item.MessageID]
		if !ok {
			continue
		}
		item.Account = account
		items = append(items, item)
	}
	return items, nil
}

func pending(ctx context.Context, feed communications.Feed, q db.Querier, account string, after int64, limit int) ([]communications.Message, error) {
	for {
		page, err := feed.Messages(ctx, q, communications.Query{Account: account, Inbox: true, Unseen: true, Received: true,
			Present: true, AfterID: after, Limit: limit})
		if err != nil || len(page) == 0 {
			return nil, err
		}
		ids := make([]int64, len(page))
		for i, m := range page {
			ids[i] = m.ID
		}
		encoded, err := json.Marshal(ids)
		if err != nil {
			return nil, err
		}
		rows, err := q.QueryContext(ctx, "SELECT message_id FROM event_candidates WHERE message_id IN (SELECT value FROM json_each(?))",
			string(encoded))
		if err != nil {
			return nil, err
		}
		marked, err := db.Collect(rows, func(r *sql.Rows) (int64, error) {
			var id int64
			return id, r.Scan(&id)
		})
		if err != nil {
			return nil, err
		}
		out := slices.DeleteFunc(page, func(m communications.Message) bool { return slices.Contains(marked, m.ID) })
		if len(out) > 0 {
			return out, nil
		}
		after = ids[len(ids)-1]
	}
}

func defaultCalendar(ctx context.Context, account string) calendar.Target {
	if config.From(ctx).Preset(account) == config.Gmail {
		return calendar.Google
	}
	return calendar.ICloud
}

func messageInvites(ctx context.Context, feed communications.Feed, q db.Querier, m communications.Message, now time.Time) ([]calendar.Invite, error) {
	parts, err := feed.Parts(ctx, q, m.ID)
	if err != nil {
		return nil, err
	}
	var out []calendar.Invite
	seen := map[string]bool{}
	for _, part := range parts {
		if !calendarTypes[part.ContentType] && !strings.HasSuffix(strings.ToLower(part.Filename), ".ics") {
			continue
		}
		data, err := feed.Part(ctx, q, m.ID, part.ID, invitationCap)
		if errors.Is(err, communications.ErrTooLarge) {
			log.Warn(fmt.Sprintf("message %d: calendar part %s skipped: larger than %d bytes", m.ID, part.ID, invitationCap))
			continue
		}
		if err != nil {
			return nil, err
		}
		if len(data) == 0 {
			continue
		}
		for _, invite := range calendar.InvitesIn(data, part.Params["method"], now) {
			if !seen[invite.UID] {
				seen[invite.UID] = true
				out = append(out, invite)
			}
		}
	}
	return out, nil
}

func proposeInvites(ctx context.Context, p *calendar.Proposals, m communications.Message, invites []calendar.Invite) (int, error) {
	made := 0
	for _, invite := range invites {
		source := map[string]any{"message_id": m.ID, "subject": m.Subject, "from": m.From.Addr, "invitation": true}
		_, created, err := p.Propose(ctx, invite.Raw(defaultCalendar(ctx, m.Account)), source, "ics:"+invite.UID)
		var refused calendar.ProposalError
		if errors.As(err, &refused) {
			log.Warn(fmt.Sprintf("message %d: invitation %s skipped: %s", m.ID, invite.UID, refused))
			continue
		}
		if err != nil {
			return made, err
		}
		if created {
			made++
		}
	}
	return made, nil
}

type Verdict int

const (
	Skip Verdict = iota
	Waiting
	NoAnswer
	NoEvent
	Event
)

type Judge interface {
	Stopped() bool
	JudgeEvent(ctx context.Context, m communications.Message, body string) (Verdict, error)
}

type scanner struct {
	d         *db.DB
	feed      communications.Feed
	judge     Judge
	proposals *calendar.Proposals
	now       func() float64
}

func ScanAccount(ctx context.Context, d *db.DB, account string, judge Judge, env config.Env, now func() float64) (proposed, flagged int, err error) {
	if env == nil {
		env = config.OS
	}
	if now == nil {
		now = wallClock
	}
	limit, _, err := calendar.CandidateLimits(ctx, d.Read)
	if err != nil {
		return 0, 0, err
	}
	feed := module.Communications(ctx)
	s := scanner{d: d, feed: feed, judge: judge, proposals: &calendar.Proposals{DB: d, Now: now, Env: env}, now: now}
	var after int64
	processed := 0
	for processed < limit {
		messages, err := pending(ctx, feed, d.Read, account, after, limit)
		if err != nil || len(messages) == 0 {
			return proposed, flagged, err
		}
		for _, m := range messages {
			if processed >= limit {
				break
			}
			after = m.ID
			made, event, waiting, err := s.scan(ctx, m)
			proposed += made
			if event {
				flagged++
			}
			if err != nil {
				return proposed, flagged, err
			}
			if !waiting {
				processed++
			}
		}
	}
	return proposed, flagged, nil
}

func (s scanner) scan(ctx context.Context, m communications.Message) (proposed int, flagged, waiting bool, err error) {
	invites, err := messageInvites(ctx, s.feed, s.d.Read, m, time.Unix(0, int64(s.now()*1e9)))
	if err != nil {
		return 0, false, false, err
	}
	if len(invites) > 0 {
		made, err := proposeInvites(ctx, s.proposals, m, invites)
		if err != nil {
			return made, false, false, err
		}
		return made, false, false, set(ctx, s.d, m.ID, StatusProposed, "calendar invitation", s.now())
	}
	if s.judge.Stopped() {
		return 0, false, false, nil
	}
	body, err := s.feed.Body(ctx, s.d.Read, m.ID)
	if err != nil {
		return 0, false, false, err
	}
	verdict, err := s.judge.JudgeEvent(ctx, m, body)
	if err != nil {
		return 0, false, false, err
	}
	switch verdict {
	case Skip:
		return 0, false, false, nil
	case Waiting:
		return 0, false, true, nil
	case NoAnswer:
		return 0, false, false, set(ctx, s.d, m.ID, StatusNone, "model gave no usable answer", s.now())
	case Event:
		return 0, true, false, set(ctx, s.d, m.ID, StatusQueued, "model flagged an event", s.now())
	}
	return 0, false, false, set(ctx, s.d, m.ID, StatusNone, "", s.now())
}

func wallClock() float64 { return float64(time.Now().UnixNano()) / 1e9 }

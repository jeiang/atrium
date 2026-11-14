package candidates

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/calendar"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/classify"
	"github.com/jeiang/atrium/internal/mail/message"
	"github.com/jeiang/atrium/internal/mail/mime"
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
	rows, err := q.QueryContext(ctx,
		"SELECT e.message_id, m.account, e.reason FROM event_candidates e"+mail.JoinClause("m", "m.id = e.message_id")+
			" WHERE e.status = ? ORDER BY e.ts, e.message_id LIMIT ?", string(StatusQueued), limit)
	if err != nil {
		return nil, err
	}
	items, err := db.Collect(rows, func(r *sql.Rows) (Item, error) {
		var item Item
		return item, r.Scan(&item.MessageID, &item.Account, &item.Reason)
	})
	if items == nil {
		items = []Item{}
	}
	return items, err
}

type row struct {
	id       int64
	account  string
	subject  string
	fromName string
	fromAddr string
	rawPath  string
}

func pending(ctx context.Context, d *db.DB, account string, after int64, limit int) ([]row, error) {
	rows, err := d.Read.QueryContext(ctx,
		"SELECT m.id, m.account, m.subject, m.from_name, m.from_addr, m.raw_path FROM "+mail.FromClause("m")+" WHERE m.account = ? AND m.in_inbox = 1 "+
			"AND m.seen = 0 AND m.sent = 0 AND m.gone_at IS NULL AND m.id > ? AND NOT EXISTS (SELECT 1 FROM event_candidates e WHERE e.message_id = m.id) "+
			"ORDER BY m.id LIMIT ?", account, after, limit)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (row, error) {
		var out row
		var raw sql.NullString
		err := r.Scan(&out.id, &out.account, &out.subject, &out.fromName, &out.fromAddr, &raw)
		out.rawPath = raw.String
		return out, err
	})
}

func defaultCalendar(ctx context.Context, account string) calendar.Target {
	if config.From(ctx).Preset(account) == config.Gmail {
		return calendar.Google
	}
	return calendar.ICloud
}

func rowInvites(cfg config.Config, r row, now time.Time) ([]calendar.Invite, error) {
	path, ok := message.RawFile(cfg.CacheDir, r.rawPath)
	if !ok {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	msg := mime.Parse(data)
	var out []calendar.Invite
	seen := map[string]bool{}
	var walk func(part *mime.Message)
	walk = func(part *mime.Message) {
		collect(part, now, seen, &out)
		for _, sub := range part.Parts() {
			walk(sub)
		}
	}
	walk(msg)
	return out, nil
}

func collect(part *mime.Message, now time.Time, seen map[string]bool, out *[]calendar.Invite) {
	if !calendarTypes[part.ContentType()] && !strings.HasSuffix(strings.ToLower(part.Filename()), ".ics") {
		return
	}
	data := part.Payload()
	if len(data) == 0 {
		return
	}
	for _, invite := range calendar.InvitesIn(data, part.Param("method"), now) {
		if !seen[invite.UID] {
			seen[invite.UID] = true
			*out = append(*out, invite)
		}
	}
}

func proposeInvites(ctx context.Context, p *calendar.Proposals, r row, invites []calendar.Invite) (int, error) {
	made := 0
	for _, invite := range invites {
		source := map[string]any{"message_id": r.id, "subject": r.subject, "from": r.fromAddr, "invitation": true}
		_, created, err := p.Propose(ctx, invite.Raw(defaultCalendar(ctx, r.account)), source, "ics:"+invite.UID)
		var refused calendar.ProposalError
		if errors.As(err, &refused) {
			log.Warn(fmt.Sprintf("message %d: invitation %s skipped: %s", r.id, invite.UID, refused))
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

type scanner struct {
	d         *db.DB
	cfg       config.Config
	account   string
	judge     *classify.Service
	paid      *classify.Deferral
	proposals *calendar.Proposals
	now       func() float64
}

func ScanAccount(ctx context.Context, d *db.DB, cfg config.Config, account string, judge *classify.Service, paid *classify.Deferral, env config.Env, now func() float64) (proposed, flagged int, err error) {
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
	s := scanner{d: d, cfg: cfg, account: account, judge: judge, paid: paid, proposals: &calendar.Proposals{DB: d, Now: now, Env: env}, now: now}
	var after int64
	processed := 0
	for processed < limit {
		rows, err := pending(ctx, d, account, after, limit)
		if err != nil || len(rows) == 0 {
			return proposed, flagged, err
		}
		for _, r := range rows {
			if processed >= limit {
				break
			}
			after = r.id
			made, event, waiting, err := s.scan(ctx, r)
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

func (s scanner) scan(ctx context.Context, r row) (proposed int, flagged, waiting bool, err error) {
	invites, err := rowInvites(s.cfg, r, time.Unix(0, int64(s.now()*1e9)))
	if err != nil {
		return 0, false, false, err
	}
	if len(invites) > 0 {
		made, err := proposeInvites(ctx, s.proposals, r, invites)
		if err != nil {
			return made, false, false, err
		}
		return made, false, false, set(ctx, s.d, r.id, StatusProposed, "calendar invitation", s.now())
	}
	if s.paid.Stopped() {
		return 0, false, false, nil
	}
	body, err := message.Body(ctx, s.d.Read, r.id)
	if err != nil {
		return 0, false, false, err
	}
	hasEvent, err := s.judge.JudgeEvent(ctx, classify.MessageItem(s.account, r.id),
		classify.Message{Subject: r.subject, FromName: r.fromName, FromAddr: r.fromAddr}, body)
	if s.paid.Note(err) {
		return 0, false, false, nil
	}
	if classify.Waiting(err) {
		return 0, false, true, nil
	}
	if final, unanswered := classify.Unanswered(err); unanswered {
		if final {
			return 0, false, false, set(ctx, s.d, r.id, StatusNone, "model gave no usable answer", s.now())
		}
		return 0, false, false, nil
	}
	if err != nil {
		return 0, false, false, err
	}
	status, reason := StatusNone, ""
	if hasEvent {
		status, reason = StatusQueued, "model flagged an event"
	}
	return 0, hasEvent, false, set(ctx, s.d, r.id, status, reason, s.now())
}

func wallClock() float64 { return float64(time.Now().UnixNano()) / 1e9 }

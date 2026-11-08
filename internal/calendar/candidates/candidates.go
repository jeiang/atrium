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
	"github.com/jeiang/atrium/internal/embed"
	"github.com/jeiang/atrium/internal/endpoint"
	"github.com/jeiang/atrium/internal/httpx"
	"github.com/jeiang/atrium/internal/llm"
	"github.com/jeiang/atrium/internal/logx"
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

type Judge func(ctx context.Context, url string, m llm.Message, body string) (bool, error)

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
		"SELECT e.message_id, m.account, e.reason FROM event_candidates e JOIN messages m ON m.id = e.message_id "+
			"WHERE e.status = ? ORDER BY e.ts, e.message_id LIMIT ?", string(StatusQueued), limit)
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

func pending(ctx context.Context, d *db.DB, account string) ([]row, error) {
	scan, _, err := calendar.CandidateLimits(ctx, d.Read)
	if err != nil {
		return nil, err
	}
	rows, err := d.Read.QueryContext(ctx,
		"SELECT m.id, m.account, m.subject, m.from_name, m.from_addr, m.raw_path FROM messages m WHERE m.account = ? AND m.in_inbox = 1 "+
			"AND m.seen = 0 AND m.sent = 0 AND m.gone_at IS NULL AND NOT EXISTS (SELECT 1 FROM event_candidates e WHERE e.message_id = m.id) "+
			"ORDER BY m.id LIMIT ?", account, scan)
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

func ScanAccount(ctx context.Context, d *db.DB, cfg config.Config, account string, judge Judge, env config.Env, now func() float64) (proposed, flagged int, err error) {
	if judge == nil {
		judge = llm.JudgeEvent
	}
	if env == nil {
		env = config.OS
	}
	if now == nil {
		now = wallClock
	}
	rows, err := pending(ctx, d, account)
	if err != nil {
		return 0, 0, err
	}
	chatURL, err := endpoint.ChatURL(ctx, d.Read)
	if err != nil {
		return 0, 0, err
	}
	ctx, err = embed.WithModels(ctx, d.Read)
	if err != nil {
		return 0, 0, err
	}
	proposals := &calendar.Proposals{DB: d, Now: now, Env: env}
	available := true
	for _, r := range rows {
		invites, err := rowInvites(cfg, r, time.Unix(0, int64(now()*1e9)))
		if err != nil {
			return proposed, flagged, err
		}
		if len(invites) > 0 {
			made, err := proposeInvites(ctx, proposals, r, invites)
			proposed += made
			if err != nil {
				return proposed, flagged, err
			}
			if err := set(ctx, d, r.id, StatusProposed, "calendar invitation", now()); err != nil {
				return proposed, flagged, err
			}
			continue
		}
		if !available {
			continue
		}
		body, err := message.Body(ctx, d.Read, r.id)
		if err != nil {
			return proposed, flagged, err
		}
		hasEvent, err := judge(ctx, chatURL, llm.Message{Subject: r.subject, FromName: r.fromName, FromAddr: r.fromAddr}, body)
		var unavailable *httpx.ServiceUnavailable
		if errors.As(err, &unavailable) {
			log.Warn(fmt.Sprintf("chat unavailable, deferring event scan: %s", unavailable))
			available = false
			continue
		}
		if err != nil {
			return proposed, flagged, err
		}
		status, reason := StatusNone, ""
		if hasEvent {
			status, reason = StatusQueued, "local model flagged an event"
			flagged++
		}
		if err := set(ctx, d, r.id, status, reason, now()); err != nil {
			return proposed, flagged, err
		}
	}
	return proposed, flagged, nil
}

func wallClock() float64 { return float64(time.Now().UnixNano()) / 1e9 }

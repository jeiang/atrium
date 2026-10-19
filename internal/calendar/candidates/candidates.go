// Package candidates is event_candidates.py: invitations in new mail become calendar proposals, other mail the
// local model flags as mentioning an event is queued, and draft_event_proposals has the agent turn the queue into
// event_propose calls.
package candidates

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/big"
	"os"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/calendar"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/httpx"
	"github.com/jeiang/atrium/internal/llm"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/mail/message"
	"github.com/jeiang/atrium/internal/mail/mime"
	"github.com/jeiang/atrium/internal/mail/router"
	"github.com/jeiang/atrium/internal/py"
)

var log = logx.Logger("atrium.event_candidates")

const (
	StatusQueued = "queued"
	Drafted      = "drafted"
	Reviewed     = "reviewed"
	Proposed     = "proposed"
	None         = "none"
	ScanLimit    = 50
	DraftLimit   = 10
)

var calendarTypes = map[string]bool{"text/calendar": true, "application/ics": true}

// Judge is llm.JudgeEvent's shape.
type Judge func(ctx context.Context, url string, m llm.Message, body string) (bool, error)

func set(ctx context.Context, d *db.DB, messageID any, status, reason string, now float64) error {
	_, err := d.Write.ExecContext(ctx, "INSERT OR REPLACE INTO event_candidates (message_id, reason, status, ts) VALUES (?, ?, ?, ?)",
		messageID, reason, status, now)
	return err
}

// Mark is mark: the candidate's status, a row of its own when the message was never a candidate.
func Mark(ctx context.Context, d *db.DB, messageID any, status string, now float64) error {
	id := messageID
	switch v := id.(type) {
	case bool:
		id = int64(0)
		if v {
			id = int64(1)
		}
	case *big.Int:
		id = v.String()
	}
	res, err := d.Write.ExecContext(ctx, "UPDATE event_candidates SET status = ?, ts = ? WHERE message_id = ?", status, now, id)
	if err != nil {
		return err
	}
	if changed, err := res.RowsAffected(); err != nil || changed > 0 {
		return err
	}
	return set(ctx, d, id, status, "", now)
}

// Item is one queued candidate.
type Item struct {
	MessageID int64  `json:"message_id"`
	Reason    string `json:"reason"`
}

// Queued is queued: the candidates waiting for the agent, oldest first.
func Queued(ctx context.Context, q db.Querier, limit int) ([]Item, error) {
	rows, err := q.QueryContext(ctx,
		"SELECT message_id, reason FROM event_candidates WHERE status = ? ORDER BY ts, message_id LIMIT ?", StatusQueued, limit)
	if err != nil {
		return nil, err
	}
	items, err := db.Collect(rows, func(r *sql.Rows) (Item, error) {
		var item Item
		return item, r.Scan(&item.MessageID, &item.Reason)
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
	rows, err := d.Read.QueryContext(ctx,
		"SELECT m.id, m.account, m.subject, m.from_name, m.from_addr, m.raw_path FROM messages m WHERE m.account = ? AND m.in_inbox = 1 "+
			"AND m.seen = 0 AND m.sent = 0 AND m.gone_at IS NULL AND NOT EXISTS (SELECT 1 FROM event_candidates e WHERE e.message_id = m.id) "+
			"ORDER BY m.id LIMIT ?", account, ScanLimit)
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

func defaultCalendar(account string) string {
	if account == "gmail" {
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
	msg, err := mime.Parse(data)
	if err != nil {
		return nil, err
	}
	var out []calendar.Invite
	seen := map[string]bool{}
	var walk func(part *mime.Message) error
	walk = func(part *mime.Message) error {
		if err := collect(part, now, seen, &out); err != nil {
			return err
		}
		for _, sub := range part.Parts() {
			if err := walk(sub); err != nil {
				return err
			}
		}
		return nil
	}
	return out, walk(msg)
}

func collect(part *mime.Message, now time.Time, seen map[string]bool, out *[]calendar.Invite) error {
	if !calendarTypes[part.ContentType()] {
		name, _, err := part.Filename()
		if err != nil {
			return err
		}
		if !strings.HasSuffix(py.Lower(name), ".ics") {
			return nil
		}
	}
	data, err := part.Payload()
	if err != nil {
		return err
	}
	if len(data) == 0 {
		return nil
	}
	method, _, err := part.Param("method", "content-type")
	if err != nil {
		method = ""
	}
	for _, invite := range calendar.InvitesIn(data, method, now) {
		if !seen[invite.UID] {
			seen[invite.UID] = true
			*out = append(*out, invite)
		}
	}
	return nil
}

func proposeInvites(ctx context.Context, p *calendar.Proposals, r row, invites []calendar.Invite) (int, error) {
	made := 0
	for _, invite := range invites {
		source := py.Dict{{Key: "message_id", Value: r.id}, {Key: "subject", Value: r.subject}, {Key: "from", Value: r.fromAddr}, {Key: "invitation", Value: true}}
		_, created, err := p.Propose(ctx, invite.Raw(defaultCalendar(r.account)), source, "ics:"+invite.UID)
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

// ScanAccount is scan_account: it parses invitations and queues event-looking mail for the new eligible messages of
// account and returns the proposals made and the messages queued. A nil judge is llm.JudgeEvent, a nil now the wall
// clock. While the chat server is down the remaining mail without an invitation is left for the next scan.
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
			if err := set(ctx, d, r.id, Proposed, "calendar invitation", now()); err != nil {
				return proposed, flagged, err
			}
			continue
		}
		if !available {
			continue
		}
		body, err := router.MessageBody(ctx, d.Read, r.id)
		if err != nil {
			return proposed, flagged, err
		}
		hasEvent, err := judge(ctx, cfg.ChatURL, llm.Message{Subject: r.subject, FromName: r.fromName, FromAddr: r.fromAddr}, body)
		var unavailable *httpx.ServiceUnavailable
		if errors.As(err, &unavailable) {
			log.Warn(fmt.Sprintf("chat unavailable, deferring event scan: %s", unavailable))
			available = false
			continue
		}
		if err != nil {
			return proposed, flagged, err
		}
		status, reason := None, ""
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

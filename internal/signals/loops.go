package signals

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	"regexp"

	"github.com/jeiang/atrium/internal/communications"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/endpoint"
	"github.com/jeiang/atrium/internal/httpx"
	"github.com/jeiang/atrium/internal/logx"
	"golang.org/x/text/cases"
)

var log = logx.Logger("atrium.openloops")

const (
	OweReply = "owe_reply"
	Waiting  = "waiting_on_them"

	Open      = "open"
	Done      = "done"
	Dismissed = "dismissed"

	KindReminder = "reminder"
	UndoReminder = "reminder_cancel"

	UndoCommitmentStatus = "commitment_status"

	Day              = 86400.0
	DefaultListLimit = 50
)

var States = [...]string{OweReply, Waiting}

var CommitmentStatuses = [...]string{Open, Done, Dismissed}

var (
	automatedPrecedence = []string{"bulk", "list", "junk"}
	automatedCategories = []string{"social", "promotions", "updates", "forums"}

	subjectPrefix = regexp.MustCompile(`^\s*(re|fwd?|aw|sv)\s*:\s*`)
	spaceRuns     = regexp.MustCompile(`\s+`)
)

func NormSubject(subject string) (string, error) {
	text := cases.Fold().String(subject)
	for {
		stripped := subjectPrefix.ReplaceAllString(text, "")
		if stripped == text {
			break
		}
		text = stripped
	}
	text = spaceRuns.ReplaceAllString(text, " ")
	return strings.TrimSpace(text), nil
}

type Message struct {
	ID         int64
	Account    string
	MsgID      string
	GmThrID    string
	InReplyTo  string
	Refs       string
	DateTS     float64
	Sent       bool
	Draft      bool
	Subject    string
	FromName   string
	FromAddr   string
	FromDomain string
	To         []string
	ListID     string
	Precedence string
	Category   string
	ESP        string
	ReplyTo    string
	Owners     []string
}

func messageOf(m communications.Message) Message {
	return Message{ID: m.ID, Account: m.Account, MsgID: m.Thread.MessageID, GmThrID: m.Thread.GmailThread,
		InReplyTo: m.Thread.InReplyTo, Refs: m.Thread.References, DateTS: m.Date, Sent: m.Flags.Sent, Draft: m.Flags.Draft,
		Subject: m.Subject, FromName: m.From.Name, FromAddr: m.From.Addr, FromDomain: m.Mailing.Domain, To: m.To,
		ListID: m.Mailing.ListID, Precedence: m.Mailing.Precedence, Category: m.Mailing.Category, ESP: m.Mailing.ESP,
		ReplyTo: m.Mailing.ReplyTo}
}

func (m *Message) Recipients() ([]string, error) {
	return m.To, nil
}

type envelope struct {
	to              []string
	cc              []string
	listUnsubscribe string
	autoSubmitted   string
	ok              bool
}

func lowerAddresses(addrs []string) []string {
	out := make([]string, 0, len(addrs))
	for _, a := range addrs {
		out = append(out, strings.ToLower(a))
	}
	return out
}

func (e envelope) bulk() bool {
	if strings.TrimSpace(e.listUnsubscribe) != "" {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(e.autoSubmitted)) {
	case "", "no":
		return false
	}
	return true
}

type gate struct {
	ctx       context.Context
	q         db.Querier
	feed      communications.Feed
	selves    map[string]bool
	envelopes map[int64]envelope
}

func newGate(ctx context.Context, q db.Querier, feed communications.Feed, selves map[string]bool) *gate {
	return &gate{ctx: ctx, q: q, feed: feed, selves: selves, envelopes: map[int64]envelope{}}
}

func (g *gate) envelope(m *Message) envelope {
	if env, ok := g.envelopes[m.ID]; ok {
		return env
	}
	var env envelope
	if found, err := g.feed.Envelope(g.ctx, g.q, m.ID); err == nil && found.Found {
		env = envelope{ok: true, to: lowerAddresses(found.To), cc: lowerAddresses(found.Cc),
			listUnsubscribe: found.ListUnsubscribe, autoSubmitted: found.AutoSubmitted}
	}
	g.envelopes[m.ID] = env
	return env
}

func bulkESP(value string) bool {
	text := strings.TrimSpace(value)
	return text != "" && text != "[]"
}

func (g *gate) automated(m *Message) (bool, error) {
	if m.FromAddr == "" || m.ListID != "" || slices.Contains(automatedPrecedence, m.Precedence) ||
		slices.Contains(automatedCategories, m.Category) || bulkESP(m.ESP) ||
		(m.ReplyTo != "" && !strings.EqualFold(m.ReplyTo, m.FromAddr)) {
		return true, nil
	}
	local, _, _ := strings.Cut(m.FromAddr, "@")
	if automated, err := communications.AutomatedLocal(local); err != nil || automated {
		return true, err
	}
	if communications.AutomatedDomain(m.FromDomain) {
		return true, nil
	}
	return g.envelope(m).bulk(), nil
}

func (g *gate) addressed(addrs []string) bool {
	for _, addr := range addrs {
		if g.selves[addr] {
			return true
		}
	}
	return false
}

func (g *gate) wanted(m *Message) (bool, error) {
	if m.Sent {
		return true, nil
	}
	automated, err := g.automated(m)
	if err != nil || automated {
		return false, err
	}
	env := g.envelope(m)
	if env.ok {
		return g.addressed(env.to) || !g.addressed(env.cc), nil
	}
	return true, nil
}

type groups struct{ parent map[string]string }

func (g *groups) find(x string) string {
	if _, ok := g.parent[x]; !ok {
		g.parent[x] = x
	}
	for g.parent[x] != x {
		g.parent[x] = g.parent[g.parent[x]]
		x = g.parent[x]
	}
	return x
}

func (g *groups) union(a, b string) {
	ra, rb := g.find(a), g.find(b)
	if ra != rb {
		g.parent[rb] = ra
	}
}

func threadGroups(rows []Message, selves map[string]bool) ([][]Message, error) {
	g := &groups{parent: map[string]string{}}
	node := func(m *Message) string { return "#" + strconv.FormatInt(m.ID, 10) }
	for i := range rows {
		r := &rows[i]
		n := node(r)
		g.find(n)
		if r.MsgID != "" {
			g.union(n, "m:"+r.MsgID)
		}
		if r.GmThrID != "" {
			g.union(n, "g:"+r.GmThrID)
		}
		links := strings.Fields(r.InReplyTo + " " + r.Refs)
		for _, ref := range links {
			g.union(n, "m:"+ref)
		}
		subject, err := NormSubject(r.Subject)
		if err != nil {
			return nil, err
		}
		if len(links) == 0 && subject != "" {
			other := r.FromAddr
			if r.Sent {
				other = ""
				to, err := r.Recipients()
				if err != nil {
					return nil, err
				}
				for _, a := range to {
					if !selves[a] {
						other = a
						break
					}
				}
			}
			g.union(n, "s:"+subject+"|"+other)
		}
	}
	index := map[string]int{}
	var found [][]Message
	for _, r := range rows {
		root := g.find(node(&r))
		i, ok := index[root]
		if !ok {
			i = len(found)
			index[root] = i
			found = append(found, nil)
		}
		found[i] = append(found[i], r)
	}
	return found, nil
}

func selfAddresses(ctx context.Context, q db.Querier, feed communications.Feed) (map[string]bool, error) {
	found, err := feed.Selves(ctx, q)
	if err != nil {
		return nil, err
	}
	selves := make(map[string]bool, len(found))
	for _, addr := range found {
		selves[strings.ToLower(addr)] = true
	}
	return selves, nil
}

type thresholds struct {
	oweMaxAge, waitingAfter, waitingMaxAge int
}

func classify(ctx context.Context, q db.Querier, thread []Message, g *gate, now float64, t thresholds) (state, counterpart string, ok bool, err error) {
	last := &thread[len(thread)-1]
	age := now - last.DateTS
	if !last.Sent {
		if age > float64(t.oweMaxAge)*Day || g.selves[last.FromAddr] {
			return "", "", false, nil
		}
		asking, err := g.feed.Asking(ctx, q, last.ID)
		if err != nil {
			return "", "", false, err
		}
		if asking != nil && !*asking {
			return "", "", false, nil
		}
		wanted, err := g.wanted(last)
		if err != nil || !wanted {
			return "", "", false, err
		}
		return OweReply, last.FromAddr, true, nil
	}
	if age < float64(t.waitingAfter)*Day || age > float64(t.waitingMaxAge)*Day {
		return "", "", false, nil
	}
	to, err := last.Recipients()
	if err != nil {
		return "", "", false, err
	}
	for _, a := range to {
		if a != "" && !g.selves[a] {
			return Waiting, a, true, nil
		}
	}
	return "", "", false, nil
}

type Counts struct {
	OweReply      int `json:"owe_reply"`
	WaitingOnThem int `json:"waiting_on_them"`
}

type override struct {
	lastMessageID int64
	snoozedUntil  *float64
	dismissed     int64
}

func SnoozeDefault(ctx context.Context, q db.Querier) (int, error) {
	r := endpoint.Read(ctx, q)
	days := r.Int(config.KeyLoopsSnoozeDefaultDays)
	return days, r.Err()
}

func Compute(ctx context.Context, d *db.DB, feed communications.Feed, now float64) (Counts, error) {
	var counts Counts
	r := endpoint.Read(ctx, d.Read)
	window := TuningOpenloopsWindowDays
	limits := thresholds{
		oweMaxAge:     TuningOpenloopsOweMaxAgeDays,
		waitingAfter:  r.Int(config.KeyLoopsWaitingAfterDays),
		waitingMaxAge: TuningOpenloopsWaitingMaxAgeDays,
	}
	if err := r.Err(); err != nil {
		return counts, err
	}
	err := d.WithTx(ctx, func(tx *sql.Tx) error {
		all, err := feed.Messages(ctx, tx, communications.Query{Present: true, Since: now - float64(window)*Day, ByDate: true})
		if err != nil {
			return err
		}
		var mail []Message
		for _, m := range all {
			if !m.Flags.Draft {
				mail = append(mail, messageOf(m))
			}
		}
		selves, err := selfAddresses(ctx, tx, feed)
		if err != nil {
			return err
		}
		g := newGate(ctx, tx, feed, selves)
		rows, err := tx.QueryContext(ctx, "SELECT thread_key FROM open_loops")
		if err != nil {
			return err
		}
		keys, err := db.Collect(rows, func(r *sql.Rows) (string, error) {
			var key string
			return key, r.Scan(&key)
		})
		if err != nil {
			return err
		}
		overrides := map[string]override{}
		rows, err = tx.QueryContext(ctx, "SELECT thread_key, last_message_id, snoozed_until, dismissed FROM loop_overrides")
		if err != nil {
			return err
		}
		_, err = db.Collect(rows, func(r *sql.Rows) (struct{}, error) {
			var key string
			var o override
			err := r.Scan(&key, &o.lastMessageID, &o.snoozedUntil, &o.dismissed)
			overrides[key] = o
			return struct{}{}, err
		})
		if err != nil {
			return err
		}
		threads, err := threadGroups(mail, selves)
		if err != nil {
			return err
		}
		keep := map[string]bool{}
		for _, thread := range threads {
			slices.SortStableFunc(thread, func(a, b Message) int {
				if c := cmpFloat(a.DateTS, b.DateTS); c != 0 {
					return c
				}
				return cmpInt(a.ID, b.ID)
			})
			state, counterpart, ok, err := classify(ctx, tx, thread, g, now, limits)
			if err != nil {
				return err
			}
			if !ok {
				continue
			}
			first, last := &thread[0], &thread[len(thread)-1]
			key := "t:" + first.MsgID
			if first.MsgID == "" {
				key = "t:id" + strconv.FormatInt(first.ID, 10)
			}
			subject := first.Subject
			if subject == "" {
				subject = last.Subject
			}
			_, err = tx.ExecContext(ctx,
				"INSERT INTO open_loops (thread_key, account, state, last_message_id, since, counterpart, subject, updated_ts) "+
					"VALUES (?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT (thread_key) DO UPDATE SET account = excluded.account, "+
					"state = excluded.state, last_message_id = excluded.last_message_id, since = excluded.since, "+
					"counterpart = excluded.counterpart, subject = excluded.subject, updated_ts = excluded.updated_ts",
				key, last.Account, state, last.ID, last.DateTS, counterpart, subject, now)
			if err != nil {
				return err
			}
			keep[key] = true
			o, has := overrides[key]
			hidden := has && o.lastMessageID == last.ID &&
				(o.dismissed != 0 || o.snoozedUntil != nil && *o.snoozedUntil > now)
			if hidden {
				continue
			}
			if state == OweReply {
				counts.OweReply++
			} else {
				counts.WaitingOnThem++
			}
		}
		for _, key := range keys {
			if !keep[key] {
				if _, err := tx.ExecContext(ctx, "DELETE FROM open_loops WHERE thread_key = ?", key); err != nil {
					return err
				}
			}
		}
		return nil
	})
	return counts, err
}

func cmpFloat(a, b float64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func cmpInt(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

type Loop struct {
	ID            int64    `json:"id"`
	ThreadKey     string   `json:"thread_key"`
	Account       string   `json:"account"`
	State         string   `json:"state"`
	LastMessageID int64    `json:"last_message_id"`
	Since         float64  `json:"since"`
	Counterpart   string   `json:"counterpart"`
	Subject       string   `json:"subject"`
	SnoozedUntil  *float64 `json:"snoozed_until"`
	Dismissed     int64    `json:"dismissed"`
	Days          int64    `json:"days"`
}

func ListLoops(ctx context.Context, q db.Querier, now float64, state string, limit int, hidden bool) ([]Loop, error) {
	var where []string
	var params []any
	if state != "" {
		where = append(where, "l.state = ?")
		params = append(params, state)
	}
	if !hidden {
		where = append(where, "COALESCE(o.dismissed, 0) = 0 AND (o.snoozed_until IS NULL OR o.snoozed_until <= ?)")
		params = append(params, now)
	}
	clause := ""
	if len(where) > 0 {
		clause = "WHERE " + strings.Join(where, " AND ")
	}
	rows, err := q.QueryContext(ctx,
		"SELECT l.rowid AS id, l.thread_key, l.account, l.state, l.last_message_id, l.since, l.counterpart, l.subject, "+
			"o.snoozed_until, COALESCE(o.dismissed, 0) FROM open_loops l LEFT JOIN loop_overrides o "+
			"ON o.thread_key = l.thread_key AND o.last_message_id = l.last_message_id "+clause+" ORDER BY l.since LIMIT ?",
		append(params, limit)...)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (Loop, error) {
		var l Loop
		err := r.Scan(&l.ID, &l.ThreadKey, &l.Account, &l.State, &l.LastMessageID, &l.Since, &l.Counterpart, &l.Subject,
			&l.SnoozedUntil, &l.Dismissed)
		l.Days = int64(math.Floor((now - l.Since) / Day))
		return l, err
	})
}

func affectedOne(res sql.Result, err error) (bool, error) {
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func Snooze(ctx context.Context, q db.Querier, id int64, until, now float64) (bool, error) {
	return affectedOne(q.ExecContext(ctx,
		"INSERT INTO loop_overrides (thread_key, last_message_id, snoozed_until, dismissed, updated_ts) "+
			"SELECT thread_key, last_message_id, ?, 0, ? FROM open_loops WHERE rowid = ? "+
			"ON CONFLICT (thread_key) DO UPDATE SET last_message_id = excluded.last_message_id, "+
			"snoozed_until = excluded.snoozed_until, updated_ts = excluded.updated_ts", until, now, id))
}

func Dismiss(ctx context.Context, q db.Querier, id int64, now float64) (bool, error) {
	return affectedOne(q.ExecContext(ctx,
		"INSERT INTO loop_overrides (thread_key, last_message_id, snoozed_until, dismissed, updated_ts) "+
			"SELECT thread_key, last_message_id, NULL, 1, ? FROM open_loops WHERE rowid = ? "+
			"ON CONFLICT (thread_key) DO UPDATE SET last_message_id = excluded.last_message_id, "+
			"dismissed = 1, updated_ts = excluded.updated_ts", now, id))
}

func DismissAll(ctx context.Context, q db.Querier, state string, now float64) (int64, error) {
	if !slices.Contains(States[:], state) {
		return 0, fmt.Errorf("%w: unknown state %s", httpx.ErrPayload, strconv.Quote(state))
	}
	res, err := q.ExecContext(ctx,
		"INSERT INTO loop_overrides (thread_key, last_message_id, snoozed_until, dismissed, updated_ts) "+
			"SELECT l.thread_key, l.last_message_id, o.snoozed_until, 1, ? FROM open_loops l "+
			"LEFT JOIN loop_overrides o ON o.thread_key = l.thread_key "+
			"WHERE l.state = ? AND COALESCE(o.dismissed, 0) = 0 "+
			"ON CONFLICT (thread_key) DO UPDATE SET last_message_id = excluded.last_message_id, "+
			"dismissed = 1, updated_ts = excluded.updated_ts", now, state)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

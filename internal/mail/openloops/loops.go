package openloops

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/httpx"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/mail/flags"
	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/rx"
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

	Day                  = 86400.0
	WindowDays           = 120
	OweMaxAgeDays        = 45
	WaitingAfterDays     = 3
	WaitingMaxAgeDays    = 60
	CommitmentWindowDays = 14
	CommitmentScanLimit  = 20
	CommitmentsPerMail   = 5
	RemindAheadDays      = 1
	RemindOverdueDays    = 14
	RemindHour           = 9
	BackfillLimit        = 2000
	SnoozeDefaultDays    = 3
	DefaultListLimit     = 50
)

var States = [...]string{OweReply, Waiting}

var CommitmentStatuses = [...]string{Open, Done, Dismissed}

var (
	automatedPrecedence = []string{"bulk", "list", "junk"}
	automatedCategories = []string{"social", "promotions", "updates", "forums"}

	subjectPrefix = rx.MustCompile(`^\s*(re|fwd?|aw|sv)\s*:\s*`, 0)
	spaceRuns     = rx.MustCompile(`\s+`, 0)
)

func NormSubject(subject string) (string, error) {
	text := py.Casefold(subject)
	for {
		stripped, err := subjectPrefix.Sub("", text, 0)
		if err != nil {
			return "", err
		}
		if stripped == text {
			break
		}
		text = stripped
	}
	text, err := spaceRuns.Sub(" ", text, 0)
	return py.Strip(text), err
}

func IsDraft(labels, flagList string) (bool, error) {
	for _, value := range [...]string{labels, flagList} {
		if !strings.Contains(value, "Draft") {
			continue
		}
		decoded, err := py.JSONLoads([]byte(value))
		if err != nil {
			return false, err
		}
		switch v := decoded.(type) {
		case []any:
			if slices.Contains(v, any(`\Draft`)) {
				return true, nil
			}
		case string:
			if strings.Contains(v, `\Draft`) {
				return true, nil
			}
		case py.Dict:
			if _, ok := v.Get(`\Draft`); ok {
				return true, nil
			}
		default:
			return false, fmt.Errorf("%w: argument of type '%s' is not iterable", httpx.ErrType, py.TypeName(decoded))
		}
	}
	return false, nil
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
	Subject    string
	FromName   string
	FromAddr   string
	ToAddrs    string
	ListID     string
	Precedence string
	Category   string
	Labels     string
	Flags      string
}

func (m *Message) Recipients() ([]string, error) {
	text := m.ToAddrs
	if text == "" {
		text = "[]"
	}
	decoded, err := py.JSONLoads([]byte(text))
	if err != nil {
		return nil, err
	}
	items, ok := decoded.([]any)
	if !ok {
		return nil, fmt.Errorf("%w: to_addrs is not a list", httpx.ErrType)
	}
	out := make([]string, len(items))
	for i, item := range items {
		if out[i], ok = item.(string); !ok {
			return nil, fmt.Errorf("%w: sequence item %d: expected str instance, %s found", httpx.ErrType, i, py.TypeName(item))
		}
	}
	return out, nil
}

func (m *Message) HumanSender() (bool, error) {
	if m.FromAddr == "" || m.ListID != "" || slices.Contains(automatedPrecedence, m.Precedence) ||
		slices.Contains(automatedCategories, m.Category) {
		return false, nil
	}
	local, _, _ := strings.Cut(m.FromAddr, "@")
	automated, err := flags.AutomatedLocal(local)
	return !automated, err
}

const messageColumns = "id, account, msgid, gm_thrid, in_reply_to, refs, date_ts, sent, subject, from_name, from_addr, to_addrs, " +
	"list_id, precedence, category, labels, flags"

func scanMessage(r *sql.Rows) (Message, error) {
	var m Message
	var gm, inReplyTo, refs sql.NullString
	err := r.Scan(&m.ID, &m.Account, &m.MsgID, &gm, &inReplyTo, &refs, &m.DateTS, &m.Sent, &m.Subject, &m.FromName, &m.FromAddr,
		&m.ToAddrs, &m.ListID, &m.Precedence, &m.Category, &m.Labels, &m.Flags)
	m.GmThrID, m.InReplyTo, m.Refs = gm.String, inReplyTo.String, refs.String
	return m, err
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
		links := py.Split(r.InReplyTo + " " + r.Refs)
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

func pendingFlag(ctx context.Context, q db.Querier, id int64) (sql.NullInt64, error) {
	var asking sql.NullInt64
	err := q.QueryRowContext(ctx, "SELECT person_asking FROM flag_decisions WHERE message_id = ?", id).Scan(&asking)
	if err == sql.ErrNoRows {
		return sql.NullInt64{}, nil
	}
	return asking, err
}

func classify(ctx context.Context, q db.Querier, thread []Message, selves map[string]bool, now float64) (state, counterpart string, ok bool, err error) {
	last := &thread[len(thread)-1]
	age := now - last.DateTS
	if !last.Sent {
		if age > OweMaxAgeDays*Day || selves[last.FromAddr] {
			return "", "", false, nil
		}
		asking, err := pendingFlag(ctx, q, last.ID)
		if err != nil {
			return "", "", false, err
		}
		if asking.Valid && asking.Int64 == 0 {
			return "", "", false, nil
		}
		if !asking.Valid {
			human, err := last.HumanSender()
			if err != nil || !human {
				return "", "", false, err
			}
		}
		return OweReply, last.FromAddr, true, nil
	}
	if age < WaitingAfterDays*Day || age > WaitingMaxAgeDays*Day {
		return "", "", false, nil
	}
	to, err := last.Recipients()
	if err != nil {
		return "", "", false, err
	}
	for _, a := range to {
		if a != "" && !selves[a] {
			return Waiting, a, true, nil
		}
	}
	return "", "", false, nil
}

type Counts struct {
	OweReply      int `json:"owe_reply"`
	WaitingOnThem int `json:"waiting_on_them"`
}

func (c Counts) Dict() py.Dict {
	return py.Dict{{Key: OweReply, Value: c.OweReply}, {Key: Waiting, Value: c.WaitingOnThem}}
}

type previousLoop struct {
	lastMessageID int64
	state         string
	snoozedUntil  *float64
	dismissed     int64
}

func Compute(ctx context.Context, d *db.DB, now float64) (Counts, error) {
	var counts Counts
	err := d.WithTx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT "+messageColumns+" FROM messages WHERE gone_at IS NULL AND date_ts >= ? ORDER BY date_ts, id",
			now-WindowDays*Day)
		if err != nil {
			return err
		}
		all, err := db.Collect(rows, scanMessage)
		if err != nil {
			return err
		}
		var mail []Message
		for _, m := range all {
			draft, err := IsDraft(m.Labels, m.Flags)
			if err != nil {
				return err
			}
			if !draft {
				mail = append(mail, m)
			}
		}
		selves := map[string]bool{}
		for _, m := range mail {
			if m.Sent && m.FromAddr != "" {
				selves[m.FromAddr] = true
			}
		}
		prev, err := tx.QueryContext(ctx, "SELECT thread_key, last_message_id, state, snoozed_until, dismissed FROM open_loops")
		if err != nil {
			return err
		}
		keys := []string{}
		previous := map[string]previousLoop{}
		_, err = db.Collect(prev, func(r *sql.Rows) (struct{}, error) {
			var key string
			var p previousLoop
			err := r.Scan(&key, &p.lastMessageID, &p.state, &p.snoozedUntil, &p.dismissed)
			keys = append(keys, key)
			previous[key] = p
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
			state, counterpart, ok, err := classify(ctx, tx, thread, selves, now)
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
			before, had := previous[key]
			same := had && before.lastMessageID == last.ID && before.state == state
			var snoozed *float64
			var dismissed int64
			if same {
				snoozed, dismissed = before.snoozedUntil, before.dismissed
			}
			subject := first.Subject
			if subject == "" {
				subject = last.Subject
			}
			_, err = tx.ExecContext(ctx,
				"INSERT INTO open_loops (thread_key, account, state, last_message_id, since, counterpart, subject, "+
					"snoozed_until, dismissed, updated_ts) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?) "+
					"ON CONFLICT (thread_key) DO UPDATE SET account = excluded.account, state = excluded.state, "+
					"last_message_id = excluded.last_message_id, since = excluded.since, counterpart = excluded.counterpart, "+
					"subject = excluded.subject, snoozed_until = excluded.snoozed_until, dismissed = excluded.dismissed, "+
					"updated_ts = excluded.updated_ts",
				key, last.Account, state, last.ID, last.DateTS, counterpart, subject, snoozed, dismissed, now)
			if err != nil {
				return err
			}
			keep[key] = true
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
		where = append(where, "l.dismissed = 0 AND (l.snoozed_until IS NULL OR l.snoozed_until <= ?)")
		params = append(params, now)
	}
	clause := ""
	if len(where) > 0 {
		clause = "WHERE " + strings.Join(where, " AND ")
	}
	rows, err := q.QueryContext(ctx,
		"SELECT l.rowid AS id, l.thread_key, l.account, l.state, l.last_message_id, l.since, l.counterpart, l.subject, "+
			"l.snoozed_until, l.dismissed FROM open_loops l "+clause+" ORDER BY l.since LIMIT ?", append(params, limit)...)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (Loop, error) {
		var l Loop
		err := r.Scan(&l.ID, &l.ThreadKey, &l.Account, &l.State, &l.LastMessageID, &l.Since, &l.Counterpart, &l.Subject,
			&l.SnoozedUntil, &l.Dismissed)
		l.Days = int64(py.FloorDiv(now-l.Since, Day))
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

func Snooze(ctx context.Context, q db.Querier, id int64, until float64) (bool, error) {
	return affectedOne(q.ExecContext(ctx, "UPDATE open_loops SET snoozed_until = ? WHERE rowid = ?", until, id))
}

func Dismiss(ctx context.Context, q db.Querier, id int64) (bool, error) {
	return affectedOne(q.ExecContext(ctx, "UPDATE open_loops SET dismissed = 1 WHERE rowid = ?", id))
}

func DismissAll(ctx context.Context, q db.Querier, state string) (int64, error) {
	if !slices.Contains(States[:], state) {
		return 0, fmt.Errorf("%w: unknown state %s", httpx.ErrValue, py.Repr(state))
	}
	res, err := q.ExecContext(ctx, "UPDATE open_loops SET dismissed = 1 WHERE state = ? AND dismissed = 0", state)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

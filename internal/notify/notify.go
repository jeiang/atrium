package notify

import (
	"context"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"regexp"
	"strings"
	"unicode"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/db/store"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/tool"
)

const NoDevices = "no push devices"

var (
	control    = regexp.MustCompile(`[\x00-\x08\x0b\x0c\x0e-\x1f\x7f]`)
	threadLink = regexp.MustCompile(`^/chat/\d+$`)
)

type Notification struct {
	ID         int64       `json:"id"`
	TS         float64     `json:"ts"`
	Title      string      `json:"title"`
	Body       string      `json:"body"`
	ThreadID   *int64      `json:"thread_id"`
	RunID      *int64      `json:"run_id"`
	ThreadLink *string     `json:"thread_link"`
	Channel    string      `json:"channel"`
	Delivered  store.Flag  `json:"delivered"`
	Error      *string     `json:"error"`
	Priority   *int64      `json:"priority"`
	Actions    []ActionRef `json:"actions"`
	ReadAt     *float64    `json:"read_at"`
}

type Action struct {
	Action string `json:"action"`
	Label  string `json:"label"`
	URL    string `json:"url"`
	Method string `json:"method,omitempty"`
}

type ActionRef struct {
	ID        string   `json:"id"`
	Kind      string   `json:"kind"`
	TargetID  int64    `json:"target_id"`
	Decision  string   `json:"decision"`
	ExpiresAt *float64 `json:"expires_at,omitempty"`
	Label     string   `json:"label"`
}

type Presenter func(ctx context.Context, d *db.DB, refs []ActionRef, now float64) ([]Action, error)

type Message struct {
	Title, Body string
	ThreadID    int64
	RunID       int64
	ProposalID  int64
	ThreadLink  *string
	Now         func() float64
	Channel     string
	Click       string
	Priority    int
	Actions     []Action
	Refs        []ActionRef
	Occurrence  string
	Mark        func(context.Context, *sql.Tx) error
}

type Sent struct {
	ID        int64
	Delivered bool
	Error     string
	Duplicate bool
}

func Plain(text string, limit int) string {
	text = strings.TrimSpace(text)
	cleaned := control.ReplaceAllString(text, "")
	return tool.Clip(cleaned, limit)
}

func LinkFor(raw *string, threadID int64) (string, error) {
	if raw == nil {
		if threadID != 0 {
			return fmt.Sprintf("/chat/%d", threadID), nil
		}
		return "", nil
	}
	link := strings.TrimSpace(*raw)
	if isDigits(link) {
		link = "/chat/" + link
	}
	if !threadLink.MatchString(link) {
		return "", tool.Error("thread_link must be a thread id or /chat/<id>")
	}
	return link, nil
}

func isDigits(s string) bool {
	return s != "" && strings.IndexFunc(s, func(r rune) bool { return !unicode.Is(unicode.Nd, r) }) < 0
}

type delivery struct {
	channel, title, body, click string
	priority                    int
	actions                     []Action
}

func deliver(ctx context.Context, d *db.DB, id int64, m delivery, link string) (bool, string, error) {
	tried, delivered, failure, err := deliverPush(ctx, d, id, m, link)
	if err == nil && !tried {
		failure = NoDevices
		pushLog.Info("no push devices; notification kept in the inbox")
	}
	return delivered, failure, err
}

func Resend(ctx context.Context, d *db.DB, id int64, present Presenter) (Sent, error) {
	var title, body, actions string
	var link, channel, click *string
	var priority *int64
	var delivered bool
	err := d.Read.QueryRowContext(ctx, "SELECT title, body, thread_link, channel, click, priority, delivered, actions_json FROM notifications WHERE id = ?", id).
		Scan(&title, &body, &link, &channel, &click, &priority, &delivered, &actions)
	if errors.Is(err, sql.ErrNoRows) {
		return Sent{}, sql.ErrNoRows
	}
	if err != nil {
		return Sent{}, err
	}
	if delivered {
		return Sent{ID: id, Delivered: true}, nil
	}
	m := delivery{channel: deref(channel), title: title, body: body, click: deref(click)}
	if m.channel == "" {
		m.channel = Assistant
	}
	if priority != nil {
		m.priority = int(*priority)
	}
	if err := ledger.CheckAdmission(ctx); err != nil {
		return Sent{}, err
	}
	var refs []ActionRef
	if err := json.Unmarshal([]byte(actions), &refs); err != nil {
		return Sent{}, err
	}
	n, err := deviceCount(ctx, d.Read)
	if err != nil {
		return Sent{}, err
	}
	if n > 0 && len(refs) > 0 && present != nil {
		if m.actions, err = present(ctx, d, refs, stamp()); err != nil {
			return Sent{}, err
		}
	}
	ok, failure, err := deliver(ctx, d, id, m, deref(link))
	if err != nil {
		return Sent{}, err
	}
	if _, err := d.Write.ExecContext(ctx, "UPDATE notifications SET delivered = ?, error = ? WHERE id = ?", ok, nullable(failure), id); err != nil {
		return Sent{}, err
	}
	return Sent{ID: id, Delivered: ok, Error: failure}, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func errorClass(err error) string {
	var op *net.OpError
	var cert x509.UnknownAuthorityError
	var host x509.HostnameError
	var invalid x509.CertificateInvalidError
	var dns *net.DNSError
	switch {
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return "protocol error"
	case strings.Contains(err.Error(), "TLS handshake timeout"):
		return "connect timeout"
	case errors.As(err, &op) && op.Op == "dial":
		if op.Timeout() {
			return "connect timeout"
		}
		return "connect failed"
	case errors.As(err, &cert), errors.As(err, &host), errors.As(err, &invalid), errors.As(err, &dns):
		return "connect failed"
	}
	var timeouter interface{ Timeout() bool }
	if errors.As(err, &timeouter) && timeouter.Timeout() {
		return "read timeout"
	}
	return "read failed"
}

func Send(ctx context.Context, d *db.DB, m Message) (Sent, error) {
	title, body := Plain(m.Title, TuningNotifyTitleChars), Plain(m.Body, TuningNotifyBodyChars)
	if title == "" {
		return Sent{}, tool.Error("title is required")
	}
	link, err := LinkFor(m.ThreadLink, m.ThreadID)
	if err != nil {
		return Sent{}, err
	}
	var linkValue any
	if link != "" {
		linkValue = link
	}
	now := m.Now
	if now == nil {
		now = stamp
	}
	channel := m.Channel
	if channel == "" {
		channel = Assistant
	}
	if !known(channel) {
		return Sent{}, ChannelError("unknown channel " + channel)
	}
	occurrence := strings.TrimSpace(m.Occurrence)
	if occurrence == "" {
		occurrence = "notify:" + ledger.NewKey()
	}
	target := "notify:" + occurrence
	refs := m.Refs
	if refs == nil {
		refs = []ActionRef{}
	}
	actions, err := json.Marshal(refs)
	if err != nil {
		return Sent{}, err
	}
	action, err := json.Marshal(map[string]any{
		"title": title, "body": body, "channel": channel, "click": m.Click, "priority": m.Priority,
	})
	if err != nil {
		return Sent{}, err
	}
	var id int64
	_, err = ledger.NewStore(d, now).ApplyLocal(ctx, ledger.New{
		Origin: "notify", Operation: "notify_inbox", OperationKey: occurrence, Target: target,
		ProposalID: idPtr(m.ProposalID), ThreadID: idPtr(m.ThreadID), RunID: idPtr(m.RunID),
		Action:    action,
		Resources: []ledger.Resource{{Key: target, RetainOnDone: true}},
	}, func(ctx context.Context, tx *sql.Tx) (ledger.Result, error) {
		res, err := tx.ExecContext(ctx, "INSERT INTO notifications (ts, title, body, thread_id, run_id, thread_link, channel, click, priority, actions_json) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
			now(), title, body, nullID(m.ThreadID), nullID(m.RunID), linkValue, nullable(m.Channel), nullable(m.Click), nullID(int64(m.Priority)), string(actions))
		if err != nil {
			return ledger.Result{}, err
		}
		id, err = res.LastInsertId()
		if err != nil {
			return ledger.Result{}, err
		}
		if m.Mark != nil {
			if err := m.Mark(ctx, tx); err != nil {
				return ledger.Result{}, err
			}
		}
		after, err := json.Marshal(map[string]any{"notification_id": id, "channel": channel})
		if err != nil {
			return ledger.Result{}, err
		}
		return ledger.Result{State: ledger.Done, Target: target, After: after}, nil
	})
	if err != nil {
		var conflict *ledger.Conflict
		if errors.As(err, &conflict) {
			return Sent{Duplicate: true}, nil
		}
		return Sent{}, err
	}
	delivered, failure, err := deliver(ctx, d, id, delivery{channel: channel, title: title, body: body, click: m.Click, priority: m.Priority, actions: m.Actions}, link)
	if err != nil {
		return Sent{}, err
	}
	if _, err := d.Write.ExecContext(ctx, "UPDATE notifications SET delivered = ?, error = ? WHERE id = ?", delivered, nullable(failure), id); err != nil {
		return Sent{}, err
	}
	return Sent{ID: id, Delivered: delivered, Error: failure}, nil
}

func idPtr(id int64) *int64 {
	if id == 0 {
		return nil
	}
	return &id
}

func nullID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

func MarkRead(ctx context.Context, q db.Querier, id int64, read bool, now float64) (bool, error) {
	if err := ledger.CheckAdmission(ctx); err != nil {
		return false, err
	}
	var at any
	if read {
		at = now
	}
	res, err := q.ExecContext(ctx, "UPDATE notifications SET read_at = CASE WHEN ? IS NULL THEN NULL ELSE COALESCE(read_at, ?) END WHERE id = ?", at, at, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func Recent(ctx context.Context, q db.Querier, limit int) ([]Notification, error) {
	return RecentBefore(ctx, q, 0, limit)
}

func RecentBefore(ctx context.Context, q db.Querier, before int64, limit int) ([]Notification, error) {
	query, args := "SELECT id, ts, title, body, thread_id, run_id, thread_link, COALESCE(channel, ''), delivered, error, priority, actions_json, read_at FROM notifications", []any{}
	if before > 0 {
		query += " WHERE id < ?"
		args = append(args, before)
	}
	rows, err := q.QueryContext(ctx, query+" ORDER BY id DESC LIMIT ?", append(args, limit)...)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (Notification, error) {
		var n Notification
		var threadID, runID sql.NullInt64
		var link, failure sql.NullString
		var delivered int64
		var actions string
		if err := r.Scan(&n.ID, &n.TS, &n.Title, &n.Body, &threadID, &runID, &link, &n.Channel, &delivered, &failure, &n.Priority, &actions, &n.ReadAt); err != nil {
			return n, err
		}
		if err := json.Unmarshal([]byte(actions), &n.Actions); err != nil {
			return n, err
		}
		n.Delivered = store.Flag(delivered != 0)
		if threadID.Valid {
			n.ThreadID = &threadID.Int64
		}
		if runID.Valid {
			n.RunID = &runID.Int64
		}
		if link.Valid {
			n.ThreadLink = &link.String
		}
		if failure.Valid {
			n.Error = &failure.String
		}
		return n, nil
	})
}

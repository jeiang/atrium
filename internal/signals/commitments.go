package signals

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/assistant/classifier"
	"github.com/jeiang/atrium/internal/communications"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/endpoint"
	"github.com/jeiang/atrium/internal/httpx"
	"github.com/jeiang/atrium/internal/jsonx"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/textx"
	"github.com/jeiang/atrium/internal/timex"
)

const CommitmentSystem = "You read one email for its owner and list explicit commitments and deadlines that belong to the mailbox " +
	"owner. A commitment is a clear promise to do something: owner 'me' when the mailbox owner promises or must act, " +
	"owner 'them' when the other party explicitly promises the owner something. Extract only items addressed to or " +
	"requested of the mailbox owner; when several people are addressed, ignore items meant for other recipients. " +
	"Include a deadline only as an explicit calendar date written YYYY-MM-DD, resolved from a date the message states " +
	"(for example 'by March 3'); when no explicit date is given set due to null. Never invent dates. Skip newsletters, " +
	"marketing, receipts, shipping notices, notifications and automated mail, and skip vague pleasantries ('let's catch " +
	"up'). Return an empty list when there is nothing."

var commitmentSchema = json.RawMessage(`{"type":"object","properties":{"commitments":{"type":"array","items":{"type":"object",` +
	`"properties":{"text":{"type":"string"},"owner":{"type":"string","enum":["me","them"]},"due":{"type":["string","null"]}},` +
	`"required":["text","owner","due"],"additionalProperties":false}}},"required":["commitments"],"additionalProperties":false}`)

func DueDate(value any) (string, bool) {
	text, isStr := value.(string)
	if !isStr {
		return "", false
	}
	parsed, ok := timex.ParseDay(textx.Clip(strings.TrimSpace(text), 10))
	if !ok {
		return "", false
	}
	year, month, day := parsed.Year(), int(parsed.Month()), parsed.Day()
	return fmt.Sprintf("%04d-%02d-%02d", year, month, day), true
}

func JudgeCommitments(ctx context.Context, env config.Env, c *classifier.Service, m Message, body string) (any, error) {
	direction := "received by the mailbox owner"
	if m.Sent {
		direction = "sent by the mailbox owner"
	}
	zone, err := config.Zone(config.From(ctx).TZName(env))
	if err != nil {
		return nil, fmt.Errorf("%w: %s", httpx.ErrMissing, err)
	}
	to, err := m.Recipients()
	if err != nil {
		return nil, err
	}
	sec, _ := timex.SplitTimestamp(m.DateTS)
	sentOn := time.Unix(sec, 0).In(zone).Format("2006-01-02")
	owners := strings.Join(m.Owners, ", ")
	if owners == "" {
		owners = "unknown"
	}
	user := fmt.Sprintf("The mailbox owner is: %s\nThis message was %s on %s.\nFrom: %s <%s>\nRecipients: %s\nSubject: %s\nBody:\n%s",
		owners, direction, sentOn, m.FromName, m.FromAddr, strings.Join(to, ", "), m.Subject, textx.Clip(body, endpoint.TuningLlmBodyChars))
	raw, err := c.Classify(ctx, classifier.Request{Caller: classifier.Commitments, Item: classifier.MessageItem(m.Account, m.ID),
		System: CommitmentSystem, User: user, Schema: commitmentSchema})
	if err != nil {
		return nil, err
	}
	var answer any
	if err := json.Unmarshal(raw, &answer); err != nil {
		return nil, fmt.Errorf("%w: %w", httpx.ErrPayload, err)
	}
	if reply, ok := answer.(map[string]any); ok {
		found, ok := reply["commitments"]
		if !ok {
			return nil, fmt.Errorf("%w: 'commitments'", httpx.ErrMissing)
		}
		return found, nil
	}
	return httpx.Key(answer, "commitments")
}

func commitmentItems(found any, perMail int) ([]any, error) {
	if !jsonx.Truthy(found) {
		return nil, nil
	}
	switch v := found.(type) {
	case []any:
		return v[:min(len(v), perMail)], nil
	case string:
		return nil, nil
	case map[string]any:
		return nil, fmt.Errorf("%w: commitment list must be a list", httpx.ErrShape)
	}
	return nil, fmt.Errorf("%w: %s is not a list", httpx.ErrShape, fmt.Sprintf("%T", found))
}

func ScanCommitments(ctx context.Context, d *db.DB, feed communications.Feed, env config.Env, now float64, c *classifier.Service, paid communications.Deferral) (int64, error) {
	window := TuningOpenloopsCommitmentWindowDays
	perMail := TuningOpenloopsCommitmentsPerMessage
	limit := TuningOpenloopsCommitmentScanLimit
	selves, err := selfAddresses(ctx, d.Read, feed)
	if err != nil {
		return 0, err
	}
	owners := make([]string, 0, len(selves))
	for addr := range selves {
		owners = append(owners, addr)
	}
	slices.Sort(owners)
	g := newGate(ctx, d.Read, feed, selves)
	result, err := feed.Scan(ctx, d, now, communications.Scan{
		Kind: "commitments", WindowDays: window, FetchLimit: limit * 4, Limit: limit, Paid: paid,
		Eligible: func(m communications.Message) (bool, error) {
			if m.Flags.Draft {
				return false, nil
			}
			msg := messageOf(m)
			return g.wanted(&msg)
		},
		Judge: func(ctx context.Context, row communications.Message, body string) (int, error) {
			if paid != nil {
				if err := paid.Err(); err != nil {
					return 0, err
				}
			}
			msg := messageOf(row)
			msg.Owners = owners
			found, err := JudgeCommitments(ctx, env, c, msg, body)
			if err != nil {
				return 0, err
			}
			items, err := commitmentItems(found, perMail)
			if err != nil {
				return 0, err
			}
			var stored int64
			for _, item := range items {
				n, err := storeCommitment(ctx, d, row.ID, item, now)
				if err != nil {
					return int(stored), err
				}
				stored += n
			}
			return int(stored), nil
		},
	})
	return int64(result.Found), err
}

func storeCommitment(ctx context.Context, d *db.DB, messageID int64, item any, now float64) (int64, error) {
	entry, ok := item.(map[string]any)
	if !ok {
		return 0, nil
	}
	text := ""
	if v := entry["text"]; jsonx.Truthy(v) {
		s, isStr := v.(string)
		if !isStr {
			return 0, fmt.Errorf("%w: commitment text is not a string", httpx.ErrShape)
		}
		text = strings.TrimSpace(s)
	}
	owner, _ := entry["owner"].(string)
	if text == "" || (owner != "me" && owner != "them") {
		return 0, nil
	}
	var due *string
	raw := entry["due"]
	if s, ok := DueDate(raw); ok {
		due = &s
	}
	res, err := d.Write.ExecContext(ctx, "INSERT OR IGNORE INTO commitments (message_id, text, owner, due, created_ts) VALUES (?, ?, ?, ?, ?)",
		messageID, textx.Clip(text, 300), owner, due, now)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

type Commitment struct {
	ID         int64   `json:"id"`
	MessageID  int64   `json:"message_id"`
	Text       string  `json:"text"`
	Owner      string  `json:"owner"`
	Due        *string `json:"due"`
	Status     string  `json:"status"`
	ProposalID *int64  `json:"proposal_id"`
	Subject    *string `json:"subject"`
	FromAddr   *string `json:"from_addr"`
}

func ListCommitments(ctx context.Context, q db.Querier, status string, limit int) ([]Commitment, error) {
	where := ""
	var params []any
	if status != "" {
		where, params = "WHERE c.status = ?", []any{status}
	}
	rows, err := q.QueryContext(ctx,
		"SELECT c.id, c.message_id, c.text, c.owner, c.due, c.status, c.proposal_id "+
			"FROM commitments c "+where+" ORDER BY c.due IS NULL, c.due, c.id LIMIT ?", append(params, limit)...)
	if err != nil {
		return nil, err
	}
	found, err := db.Collect(rows, func(r *sql.Rows) (Commitment, error) {
		var c Commitment
		err := r.Scan(&c.ID, &c.MessageID, &c.Text, &c.Owner, &c.Due, &c.Status, &c.ProposalID)
		return c, err
	})
	if err != nil {
		return nil, err
	}
	feed := module.Communications(ctx)
	for i := range found {
		m, err := feed.Message(ctx, q, found[i].MessageID)
		if err != nil {
			return nil, err
		}
		if m != nil {
			found[i].Subject, found[i].FromAddr = &m.Subject, &m.From.Addr
		}
	}
	return found, nil
}

func counterpart(m *communications.Message) *string {
	switch {
	case m == nil:
		return nil
	case m.Flags.Sent:
		if len(m.To) == 0 {
			return nil
		}
		return &m.To[0]
	case m.From.Addr == "":
		return nil
	}
	return &m.From.Addr
}

func withMessage(ctx context.Context, q db.Querier, c CommitmentRow) (CommitmentRow, error) {
	m, err := module.Communications(ctx).Message(ctx, q, c.MessageID)
	if err != nil {
		return c, err
	}
	if m != nil {
		c.Subject = &m.Subject
	}
	c.Counterpart = counterpart(m)
	return c, nil
}

func SetCommitmentStatus(ctx context.Context, q db.Querier, id int64, status string) (bool, error) {
	known := false
	for _, s := range CommitmentStatuses {
		known = known || s == status
	}
	if !known {
		return false, fmt.Errorf("%w: unknown status %s", httpx.ErrPayload, strconv.Quote(status))
	}
	return affectedOne(q.ExecContext(ctx, "UPDATE commitments SET status = ? WHERE id = ?", status, id))
}

type CommitmentRow struct {
	ID          int64
	MessageID   int64
	Text        string
	Owner       string
	Due         *string
	Status      string
	ProposalID  *int64
	CreatedTS   float64
	Subject     *string
	Counterpart *string
}

const commitmentRowSelect = "SELECT c.id, c.message_id, c.text, c.owner, c.due, c.status, c.proposal_id, c.created_ts FROM commitments c"

func scanCommitmentRow(r interface{ Scan(...any) error }) (CommitmentRow, error) {
	var c CommitmentRow
	err := r.Scan(&c.ID, &c.MessageID, &c.Text, &c.Owner, &c.Due, &c.Status, &c.ProposalID, &c.CreatedTS)
	return c, err
}

func CommitmentRows(ctx context.Context, q db.Querier) ([]CommitmentRow, error) {
	rows, err := q.QueryContext(ctx, commitmentRowSelect+" ORDER BY c.id")
	if err != nil {
		return nil, err
	}
	found, err := db.Collect(rows, func(r *sql.Rows) (CommitmentRow, error) { return scanCommitmentRow(r) })
	if err != nil {
		return nil, err
	}
	for i := range found {
		if found[i], err = withMessage(ctx, q, found[i]); err != nil {
			return nil, err
		}
	}
	return found, nil
}

func GetCommitment(ctx context.Context, q db.Querier, id int64) (*CommitmentRow, error) {
	c, err := scanCommitmentRow(q.QueryRowContext(ctx, commitmentRowSelect+" WHERE c.id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	c, err = withMessage(ctx, q, c)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

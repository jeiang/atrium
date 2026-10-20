package openloops

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/httpx"
	"github.com/jeiang/atrium/internal/llm"
	"github.com/jeiang/atrium/internal/mail/router"
	"github.com/jeiang/atrium/internal/py"
)

const CommitmentSystem = "You read one email for its owner and list explicit commitments and deadlines. A commitment is a clear promise " +
	"to do something (owner 'me' when the mailbox owner promises or must act, owner 'them' when the other party " +
	"promises or must act). Include a deadline only as an explicit calendar date written YYYY-MM-DD, resolved from " +
	"a date the message states (for example 'by March 3'); when no explicit date is given set due to null. Never " +
	"invent dates, skip vague pleasantries ('let's catch up'), newsletters and automated notices. Return an empty " +
	"list when there is nothing."

var CommitmentSchema = llm.Object{
	{Key: "type", Value: "object"},
	{Key: "properties", Value: llm.Object{
		{Key: "commitments", Value: llm.Object{
			{Key: "type", Value: "array"},
			{Key: "items", Value: llm.Object{
				{Key: "type", Value: "object"},
				{Key: "properties", Value: llm.Object{
					{Key: "text", Value: llm.Object{{Key: "type", Value: "string"}}},
					{Key: "owner", Value: llm.Object{{Key: "type", Value: "string"}, {Key: "enum", Value: []string{"me", "them"}}}},
					{Key: "due", Value: llm.Object{{Key: "type", Value: []string{"string", "null"}}}},
				}},
				{Key: "required", Value: []string{"text", "owner", "due"}},
				{Key: "additionalProperties", Value: false},
			}},
		}},
	}},
	{Key: "required", Value: []string{"commitments"}},
	{Key: "additionalProperties", Value: false},
}

func DueDate(value any) (string, bool) {
	text, isStr := value.(string)
	if !isStr {
		return "", false
	}
	year, month, day, ok := py.DateFromISOFormat(py.Clip(py.Strip(text), 10))
	if !ok {
		return "", false
	}
	return fmt.Sprintf("%04d-%02d-%02d", year, month, day), true
}

type Judge func(ctx context.Context, env config.Env, url string, m Message, body string) (any, error)

func JudgeCommitments(ctx context.Context, env config.Env, url string, m Message, body string) (any, error) {
	direction := "received by the mailbox owner"
	if m.Sent {
		direction = "sent by the mailbox owner"
	}
	zone, err := config.Zone(config.TZName(env))
	if err != nil {
		return nil, fmt.Errorf("%w: %s", httpx.ErrKey, err)
	}
	to, err := m.Recipients()
	if err != nil {
		return nil, err
	}
	sec, _ := py.SplitTimestamp(m.DateTS)
	sentOn := time.Unix(sec, 0).In(zone).Format("2006-01-02")
	user := fmt.Sprintf("This message was %s on %s.\nFrom: %s <%s>\nTo: %s\nSubject: %s\nBody:\n%s",
		direction, sentOn, m.FromName, m.FromAddr, strings.Join(to, ", "), m.Subject, py.Clip(body, config.LLMBodyChars))
	raw, err := llm.ChatJSON(ctx, url, CommitmentSystem, user, CommitmentSchema, 500)
	if err != nil {
		return nil, err
	}
	answer, err := py.JSONLoads(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", httpx.ErrValue, err)
	}
	if reply, ok := answer.(py.Dict); ok {
		found, ok := reply.Get("commitments")
		if !ok {
			return nil, fmt.Errorf("%w: 'commitments'", httpx.ErrKey)
		}
		return found, nil
	}
	return httpx.Key(answer, "commitments")
}

func scannable(ctx context.Context, q db.Querier, now float64, limit int) ([]Message, error) {
	rows, err := q.QueryContext(ctx,
		"SELECT m."+strings.ReplaceAll(messageColumns, ", ", ", m.")+" FROM messages m WHERE m.gone_at IS NULL AND m.date_ts >= ? "+
			"AND NOT EXISTS (SELECT 1 FROM messages o JOIN commitment_scans s ON s.message_id = o.id "+
			"WHERE o.account = m.account AND o.digest = m.digest) ORDER BY m.date_ts DESC LIMIT ?",
		now-CommitmentWindowDays*Day, limit*4)
	if err != nil {
		return nil, err
	}
	all, err := db.Collect(rows, scanMessage)
	if err != nil {
		return nil, err
	}
	var out []Message
	for _, m := range all {
		draft, err := IsDraft(m.Labels, m.Flags)
		if err != nil {
			return nil, err
		}
		if draft {
			continue
		}
		human := true
		if !m.Sent {
			if human, err = m.HumanSender(); err != nil {
				return nil, err
			}
		}
		if human {
			out = append(out, m)
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// commitmentItems is (found or [])[:COMMITMENTS_PER_MESSAGE]: a falsy answer has none, a list its first items; a
// string slices harmlessly (none of its characters is an object) and any other value raises TypeError.
func commitmentItems(found any) ([]any, error) {
	if !py.Truthy(found) {
		return nil, nil
	}
	switch v := found.(type) {
	case []any:
		return v[:min(len(v), CommitmentsPerMail)], nil
	case string:
		return nil, nil
	case py.Dict, map[string]any:
		return nil, fmt.Errorf("%w: unhashable type: 'slice'", httpx.ErrType)
	}
	return nil, fmt.Errorf("%w: '%s' object is not subscriptable", httpx.ErrType, py.TypeName(found))
}

func ScanCommitments(ctx context.Context, d *db.DB, cfg config.Config, env config.Env, now float64, judge Judge, limit int) (int64, error) {
	if judge == nil {
		judge = JudgeCommitments
	}
	rows, err := scannable(ctx, d.Read, now, limit)
	if err != nil {
		return 0, err
	}
	var stored int64
rows:
	for _, row := range rows {
		body, err := router.MessageBody(ctx, d.Read, row.ID)
		if err != nil {
			return stored, err
		}
		found, err := judge(ctx, env, cfg.ChatURL, row, body)
		var unavailable *httpx.ServiceUnavailable
		switch {
		case errors.As(err, &unavailable):
			log.Warn(fmt.Sprintf("chat unavailable, deferring commitment scan: %s", unavailable))
			break rows
		case errors.Is(err, httpx.ErrValue) || errors.Is(err, httpx.ErrKey) || errors.Is(err, httpx.ErrType):
			log.Warn(fmt.Sprintf("message %d: unusable commitment answer: %s", row.ID, httpx.Message(err)))
			found = nil
		case err != nil:
			return stored, err
		}
		items, err := commitmentItems(found)
		if err != nil {
			return stored, err
		}
		for _, item := range items {
			n, err := storeCommitment(ctx, d, row.ID, item, now)
			if err != nil {
				return stored, err
			}
			stored += n
		}
		if _, err := d.Write.ExecContext(ctx, "INSERT OR REPLACE INTO commitment_scans (message_id, ts) VALUES (?, ?)", row.ID, now); err != nil {
			return stored, err
		}
	}
	return stored, nil
}

func storeCommitment(ctx context.Context, d *db.DB, messageID int64, item any, now float64) (int64, error) {
	entry, ok := item.(py.Dict)
	if !ok {
		return 0, nil
	}
	text := ""
	if v, _ := entry.Get("text"); py.Truthy(v) {
		s, isStr := v.(string)
		if !isStr {
			return 0, py.AttributeError(v, "strip")
		}
		text = py.Strip(s)
	}
	owner, _ := entry.Get("owner")
	if text == "" || (owner != "me" && owner != "them") {
		return 0, nil
	}
	var due *string
	raw, _ := entry.Get("due")
	if s, ok := DueDate(raw); ok {
		due = &s
	}
	res, err := d.Write.ExecContext(ctx, "INSERT OR IGNORE INTO commitments (message_id, text, owner, due, created_ts) VALUES (?, ?, ?, ?, ?)",
		messageID, py.Clip(text, 300), owner, due, now)
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
		"SELECT c.id, c.message_id, c.text, c.owner, c.due, c.status, c.proposal_id, m.subject, m.from_addr "+
			"FROM commitments c LEFT JOIN messages m ON m.id = c.message_id "+where+
			" ORDER BY c.due IS NULL, c.due, c.id LIMIT ?", append(params, limit)...)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (Commitment, error) {
		var c Commitment
		err := r.Scan(&c.ID, &c.MessageID, &c.Text, &c.Owner, &c.Due, &c.Status, &c.ProposalID, &c.Subject, &c.FromAddr)
		return c, err
	})
}

func SetCommitmentStatus(ctx context.Context, q db.Querier, id int64, status string) (bool, error) {
	known := false
	for _, s := range CommitmentStatuses {
		known = known || s == status
	}
	if !known {
		return false, fmt.Errorf("%w: unknown status %s", httpx.ErrValue, py.Repr(status))
	}
	return affectedOne(q.ExecContext(ctx, "UPDATE commitments SET status = ? WHERE id = ?", status, id))
}

package openloops

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/embed"
	"github.com/jeiang/atrium/internal/endpoint"
	"github.com/jeiang/atrium/internal/httpx"
	"github.com/jeiang/atrium/internal/jsonx"
	"github.com/jeiang/atrium/internal/llm"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/mailscan"
	"github.com/jeiang/atrium/internal/textx"
	"github.com/jeiang/atrium/internal/timex"
)

const CommitmentSystem = "You read one email for its owner and list explicit commitments and deadlines. A commitment is a clear promise " +
	"to do something (owner 'me' when the mailbox owner promises or must act, owner 'them' when the other party " +
	"promises or must act). Include a deadline only as an explicit calendar date written YYYY-MM-DD, resolved from " +
	"a date the message states (for example 'by March 3'); when no explicit date is given set due to null. Never " +
	"invent dates, skip vague pleasantries ('let's catch up'), newsletters and automated notices. Return an empty " +
	"list when there is nothing."

var CommitmentSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"commitments": map[string]any{
			"type": "array",
			"items": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"text":  map[string]any{"type": "string"},
					"owner": map[string]any{"type": "string", "enum": []string{"me", "them"}},
					"due":   map[string]any{"type": []string{"string", "null"}},
				},
				"required":             []string{"text", "owner", "due"},
				"additionalProperties": false,
			},
		},
	},
	"required":             []string{"commitments"},
	"additionalProperties": false,
}

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

type Judge func(ctx context.Context, env config.Env, url string, m Message, body string) (any, error)

func JudgeCommitments(ctx context.Context, env config.Env, url string, m Message, body string) (any, error) {
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
	user := fmt.Sprintf("This message was %s on %s.\nFrom: %s <%s>\nTo: %s\nSubject: %s\nBody:\n%s",
		direction, sentOn, m.FromName, m.FromAddr, strings.Join(to, ", "), m.Subject, textx.Clip(body, llm.BodyChars(ctx)))
	raw, err := llm.ChatJSON(ctx, url, CommitmentSystem, user, CommitmentSchema, 500)
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

func messageOf(m mailscan.Message) Message {
	return Message{ID: m.ID, Account: m.Account, DateTS: m.DateTS, Sent: m.Sent, Subject: m.Subject, FromName: m.FromName,
		FromAddr: m.FromAddr, ToAddrs: m.ToAddrs, ListID: m.ListID, Precedence: m.Precedence, Category: m.Category,
		Labels: m.Labels, Flags: m.Flags}
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

func ScanCommitments(ctx context.Context, d *db.DB, env config.Env, now float64, judge Judge) (int64, error) {
	if judge == nil {
		judge = JudgeCommitments
	}
	r := endpoint.Read(ctx, d.Read)
	window := r.Int(mail.KeyLoopsCommitWindowDays)
	perMail := r.Int(mail.KeyLoopsCommitsPerMail)
	limit := r.Int(mail.KeyLoopsCommitScanLimit)
	if err := r.Err(); err != nil {
		return 0, err
	}
	chatURL, err := endpoint.ChatURL(ctx, d.Read)
	if err != nil {
		return 0, err
	}
	ctx, err = embed.WithModels(ctx, d.Read)
	if err != nil {
		return 0, err
	}
	result, err := mailscan.Run(ctx, d, now, mailscan.Spec{
		Kind: "commitments", Hint: "1", WindowDays: window, FetchLimit: limit * 4, Limit: limit,
		Eligible: func(m mailscan.Message) (bool, error) {
			draft, err := IsDraft(m.Labels, m.Flags)
			if err != nil || draft {
				return false, err
			}
			msg := messageOf(m)
			if msg.Sent {
				return true, nil
			}
			return msg.HumanSender()
		},
		Judge: func(ctx context.Context, row mailscan.Message, body string) (int, error) {
			found, err := judge(ctx, env, chatURL, messageOf(row), body)
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
		return false, fmt.Errorf("%w: unknown status %s", httpx.ErrPayload, strconv.Quote(status))
	}
	return affectedOne(q.ExecContext(ctx, "UPDATE commitments SET status = ? WHERE id = ?", status, id))
}

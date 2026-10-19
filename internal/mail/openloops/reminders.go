package openloops

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/assistant/scheduler"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/httpx"
	"github.com/jeiang/atrium/internal/proposals"
	"github.com/jeiang/atrium/internal/py"
)

func wallClock() float64 { return float64(time.Now().UnixNano()) / 1e9 }

// Reminders is the reminder kind over one database: it queues proposals for due commitments, and its executor
// and undoer create and cancel the one-shot schedule job. Now is the clock Python reads as db.now (the wall clock
// when nil) and Env the environment ATRIUM_TZ is read from (config.OS when nil).
type Reminders struct {
	DB  *db.DB
	Now func() float64
	Env config.Env
}

func (r *Reminders) now() float64 {
	if r.Now != nil {
		return r.Now()
	}
	return wallClock()
}

func (r *Reminders) env() config.Env {
	if r.Env != nil {
		return r.Env
	}
	return config.OS
}

func local(ts float64, loc *time.Location) time.Time {
	sec, _ := py.SplitTimestamp(ts)
	return time.Unix(sec, 0).In(loc)
}

// ReminderTime is reminder_time: the epoch of RemindHour local on the due date, or five minutes after now when
// that has passed.
func ReminderTime(due, tz string, now float64) (float64, error) {
	loc, err := scheduler.Zone(tz)
	if err != nil {
		return 0, err
	}
	year, month, day, ok := py.DateFromISOFormat(due)
	if !ok {
		return 0, fmt.Errorf("%w: Invalid isoformat string: %s", httpx.ErrValue, py.Repr(due))
	}
	wall := time.Date(year, time.Month(month), day, RemindHour, 0, 0, 0, time.UTC).Unix()
	return max(float64(py.WallToUnix(loc, wall)), now+300), nil
}

// ReminderPrompt is reminder_prompt.
func ReminderPrompt(text string, messageID int64) string {
	return fmt.Sprintf("Remind the operator: %s (source mail %d)", text, messageID)
}

// Propose is propose_reminders: a reminder proposal for each open dated commitment that is due soon or recently
// overdue, in the zone tz (ATRIUM_TZ when empty). It returns how many it created.
func (r *Reminders) Propose(ctx context.Context, now float64, tz string) (int, error) {
	if tz == "" {
		tz = config.TZName(r.env())
	}
	loc, err := scheduler.Zone(tz)
	if err != nil {
		return 0, err
	}
	at := local(now, loc)
	today := time.Date(at.Year(), at.Month(), at.Day(), 0, 0, 0, 0, time.UTC)
	latest := today.AddDate(0, 0, RemindAheadDays).Format("2006-01-02")
	earliest := today.AddDate(0, 0, -RemindOverdueDays).Format("2006-01-02")
	rows, err := r.DB.Read.QueryContext(ctx,
		"SELECT c.id, c.message_id, c.text, c.owner, c.due, m.subject FROM commitments c LEFT JOIN messages m ON m.id = c.message_id "+
			"WHERE c.status = 'open' AND c.proposal_id IS NULL AND c.due IS NOT NULL AND c.due <= ? AND c.due >= ? ORDER BY c.due, c.id",
		latest, earliest)
	if err != nil {
		return 0, err
	}
	type dueItem struct {
		id, messageID    int64
		text, owner, due string
		subject          *string
	}
	commitments, err := db.Collect(rows, func(s *sql.Rows) (dueItem, error) {
		var c dueItem
		err := s.Scan(&c.id, &c.messageID, &c.text, &c.owner, &c.due, &c.subject)
		return c, err
	})
	if err != nil {
		return 0, err
	}
	made := 0
	for _, c := range commitments {
		remindAt, err := ReminderTime(c.due, tz, now)
		if err != nil {
			return made, err
		}
		payload := py.Dict{
			{Key: "commitment_id", Value: c.id}, {Key: "text", Value: c.text}, {Key: "due", Value: c.due},
			{Key: "owner", Value: c.owner}, {Key: "message_id", Value: c.messageID}, {Key: "tz", Value: tz},
		}
		subject := ""
		if c.subject != nil {
			subject = *c.subject
		}
		preview := append(append(py.Dict{}, payload...), py.Pair{Key: "subject", Value: subject},
			py.Pair{Key: "remind_at", Value: py.ISOFormat(local(remindAt, loc), "minutes")})
		sum := sha256.Sum256([]byte(py.JSONDumps([]any{KindReminder, c.id})))
		fingerprint := hex.EncodeToString(sum[:])[:32]
		title := fmt.Sprintf("Remind me: %s (due %s)", py.Clip(c.text, 80), c.due)
		pid, created, err := proposals.Create(ctx, r.DB, KindReminder, nil, title, payload, preview, fingerprint, r.now())
		if err != nil {
			return made, err
		}
		if created {
			made++
		} else if err := r.DB.Write.QueryRowContext(ctx, "SELECT id FROM proposals WHERE fingerprint = ?", fingerprint).Scan(&pid); err != nil {
			return made, err
		}
		if _, err := r.DB.Write.ExecContext(ctx, "UPDATE commitments SET proposal_id = ? WHERE id = ?", pid, c.id); err != nil {
			return made, err
		}
	}
	return made, nil
}

func payloadField(d py.Dict, key string) (any, error) {
	v, ok := d.Get(key)
	if !ok {
		return nil, fmt.Errorf("%w: %s", httpx.ErrKey, py.Repr(key))
	}
	return v, nil
}

func payloadString(d py.Dict, key string) (string, error) {
	v, err := payloadField(d, key)
	if err != nil {
		return "", err
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("%w: %s must be a string, not %s", httpx.ErrType, key, py.TypeName(v))
	}
	return s, nil
}

// Execute is execute_reminder: on approval, the one-shot schedule job that delivers the reminder, logged with its
// reminder_cancel undo plan.
func (r *Reminders) Execute(ctx context.Context, p *proposals.Proposal, _ py.Dict) (py.Dict, error) {
	decoded, err := py.JSONLoads(p.Payload)
	if err != nil {
		return nil, err
	}
	payload, ok := decoded.(py.Dict)
	if !ok {
		return nil, fmt.Errorf("%w: reminder payload is not an object", httpx.ErrType)
	}
	text, err := payloadString(payload, "text")
	if err != nil {
		return nil, err
	}
	due, err := payloadString(payload, "due")
	if err != nil {
		return nil, err
	}
	tz, err := payloadString(payload, "tz")
	if err != nil {
		return nil, err
	}
	messageID, err := payloadField(payload, "message_id")
	if err != nil {
		return nil, err
	}
	mid, err := py.Int(messageID)
	if err != nil {
		return nil, err
	}
	current := r.now()
	remindAt, err := ReminderTime(due, tz, current)
	if err != nil {
		return nil, err
	}
	loc, err := scheduler.Zone(tz)
	if err != nil {
		return nil, err
	}
	moment := py.ISOFormat(local(remindAt, loc), "seconds")
	jobID, err := scheduler.CreateJob(ctx, r.DB.Write, scheduler.NewJob{
		Name:       "Reminder: " + py.Clip(text, 50),
		Prompt:     ReminderPrompt(text, mid.Int64()),
		Schedule:   moment,
		TZ:         tz,
		Now:        current,
		SkipLimits: true,
	})
	if err != nil {
		return nil, err
	}
	action := py.Dict{{Key: "type", Value: KindReminder}, {Key: "job_id", Value: jobID}}
	undo := py.Dict{{Key: "op", Value: UndoReminder}, {Key: "job_id", Value: jobID}}
	if _, err := proposals.RecordAction(ctx, r.DB.Write, p.ID, action, undo, true, nil, r.now()); err != nil {
		return nil, err
	}
	return py.Dict{
		{Key: "summary", Value: "Reminder scheduled for " + strings.ReplaceAll(py.Clip(moment, 16), "T", " ")},
		{Key: "job_id", Value: jobID},
	}, nil
}

// Undo is undo_reminder: it cancels the job the executor created.
func (r *Reminders) Undo(ctx context.Context, plan json.RawMessage) (py.Dict, error) {
	var info struct {
		JobID int64 `json:"job_id"`
	}
	if err := json.Unmarshal(plan, &info); err != nil {
		return nil, err
	}
	if err := scheduler.CancelJob(ctx, r.DB.Write, info.JobID); err != nil {
		return nil, err
	}
	return py.Dict{{Key: "cancelled", Value: info.JobID}}, nil
}

// Register is register: the reminder executor and its undoer.
func Register(reg *proposals.Registry, r *Reminders) {
	reg.Executors[KindReminder] = r.Execute
	reg.Undoers[UndoReminder] = r.Undo
}

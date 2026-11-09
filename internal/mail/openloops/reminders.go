package openloops

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/endpoint"
	"github.com/jeiang/atrium/internal/httpx"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/proposals"
	"github.com/jeiang/atrium/internal/tasks"
	"github.com/jeiang/atrium/internal/textx"
	"github.com/jeiang/atrium/internal/timex"
)

func wallClock() float64 { return float64(time.Now().UnixNano()) / 1e9 }

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
	sec, _ := timex.SplitTimestamp(ts)
	return time.Unix(sec, 0).In(loc)
}

func ReminderTime(due, tz string, hour int, now float64) (float64, error) {
	loc, err := config.Zone(tz)
	if err != nil {
		return 0, err
	}
	parsed, ok := timex.ParseDay(due)
	if !ok {
		return 0, fmt.Errorf("%w: Invalid isoformat string: %s", httpx.ErrPayload, strconv.Quote(due))
	}
	year, month, day := parsed.Year(), int(parsed.Month()), parsed.Day()
	wall := time.Date(year, time.Month(month), day, hour, 0, 0, 0, time.UTC).Unix()
	return max(float64(timex.WallToUnix(loc, wall)), now+300), nil
}

func ReminderPrompt(text string, messageID int64) string {
	return fmt.Sprintf("Remind the operator: %s (source mail %d)", text, messageID)
}

func (r *Reminders) Propose(ctx context.Context, now float64, tz string) (int, error) {
	if tz == "" {
		tz = config.From(ctx).TZName(r.env())
	}
	loc, err := config.Zone(tz)
	if err != nil {
		return 0, err
	}
	set := endpoint.Read(ctx, r.DB.Read)
	ahead := set.Int(mail.KeyLoopsRemindAheadDays)
	overdue := set.Int(mail.KeyLoopsRemindOverdueDays)
	hour := set.Int(mail.KeyLoopsRemindHour)
	if err := set.Err(); err != nil {
		return 0, err
	}
	at := local(now, loc)
	today := time.Date(at.Year(), at.Month(), at.Day(), 0, 0, 0, 0, time.UTC)
	latest := today.AddDate(0, 0, ahead).Format("2006-01-02")
	earliest := today.AddDate(0, 0, -overdue).Format("2006-01-02")
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
		remindAt, err := ReminderTime(c.due, tz, hour, now)
		if err != nil {
			return made, err
		}
		payload := map[string]any{
			"commitment_id": c.id, "text": c.text, "due": c.due,
			"owner": c.owner, "message_id": c.messageID, "tz": tz,
			"remind_at": remindAt,
		}
		subject := ""
		if c.subject != nil {
			subject = *c.subject
		}
		preview := map[string]any{}
		for key, value := range payload {
			preview[key] = value
		}
		preview["subject"] = subject
		preview["remind_at"] = timex.ISOFormat(local(remindAt, loc), "minutes")
		encoded, _ := json.Marshal([]any{KindReminder, c.id})
		sum := sha256.Sum256(encoded)
		fingerprint := hex.EncodeToString(sum[:])[:32]
		title := fmt.Sprintf("Remind me: %s (due %s)", textx.Clip(c.text, 80), c.due)
		pid, created, err := proposals.Create(ctx, r.DB, KindReminder, nil, title, payload, preview, fingerprint, now)
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

func payloadField(d map[string]any, key string) (any, error) {
	v, ok := d[key]
	if !ok {
		return nil, fmt.Errorf("%w: %s", httpx.ErrMissing, strconv.Quote(key))
	}
	return v, nil
}

func payloadNumber(d map[string]any, key string) (float64, error) {
	v, err := payloadField(d, key)
	if err != nil {
		return 0, err
	}
	n, ok := v.(float64)
	if !ok {
		return 0, fmt.Errorf("%w: %s must be a number", httpx.ErrShape, key)
	}
	return n, nil
}

func payloadString(d map[string]any, key string) (string, error) {
	v, err := payloadField(d, key)
	if err != nil {
		return "", err
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("%w: %s must be a string, not %s", httpx.ErrShape, key, fmt.Sprintf("%T", v))
	}
	return s, nil
}

type executeResult struct {
	Summary string `json:"summary"`
	TaskID  int64  `json:"task_id"`
}

func (r *Reminders) Execute(ctx context.Context, p *proposals.Proposal, _ any) (any, error) {
	var decoded any
	if err := json.Unmarshal(p.Payload, &decoded); err != nil {
		return nil, err
	}
	payload, ok := decoded.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%w: reminder payload is not an object", httpx.ErrShape)
	}
	text, err := payloadString(payload, "text")
	if err != nil {
		return nil, err
	}
	remindAt, err := payloadNumber(payload, "remind_at")
	if err != nil {
		return nil, err
	}
	current := r.now()
	id, err := tasks.Add(ctx, r.DB.Write, current, tasks.Spec{
		Title: text, RemindAt: &remindAt, SourceKind: "proposal", SourceID: strconv.FormatInt(p.ID, 10),
	})
	if err != nil {
		return nil, err
	}
	action := map[string]any{"type": KindReminder, "task_id": id}
	undo := map[string]any{"op": UndoReminder, "id": id}
	if _, err := proposals.RecordAction(ctx, r.DB.Write, p.ID, "write", nil, action, undo, ledger.OK, nil, r.now()); err != nil {
		return nil, err
	}
	return executeResult{Summary: "Reminder set for " + time.Unix(int64(remindAt), 0).UTC().Format("2006-01-02 15:04"), TaskID: id}, nil
}

func (r *Reminders) Undo(ctx context.Context, plan json.RawMessage) (any, error) {
	var info struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(plan, &info); err != nil {
		return nil, err
	}
	if _, err := tasks.Delete(ctx, r.DB.Write, info.ID); err != nil {
		return nil, err
	}
	return map[string]any{"deleted": info.ID}, nil
}

func Register(reg *proposals.Registry, r *Reminders, owner string) {
	reg.Executors[KindReminder] = r.Execute
	reg.Undoers[UndoReminder] = r.Undo
	reg.AddKind(proposals.Kind{Name: KindReminder, Module: owner, Label: "Reminder", Icon: "scroll-text"})
}

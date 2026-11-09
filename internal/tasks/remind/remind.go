package remind

import (
	"context"
	"fmt"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/notify"
)

func Due(ctx context.Context, d *db.DB, env config.Env, now float64) (int, error) {
	rows, err := d.Read.QueryContext(ctx,
		"SELECT id, title, notes, remind_at FROM tasks WHERE status = 'open' AND remind_at IS NOT NULL AND remind_at <= ? AND reminded_ts IS NULL ORDER BY remind_at, id", now)
	if err != nil {
		return 0, err
	}
	type due struct {
		id    int64
		title string
		notes string
		at    float64
	}
	var found []due
	for rows.Next() {
		var one due
		if err := rows.Scan(&one.id, &one.title, &one.notes, &one.at); err != nil {
			_ = rows.Close()
			return 0, err
		}
		found = append(found, one)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	sent := 0
	for _, one := range found {
		body := one.notes
		if body == "" {
			body = fmt.Sprintf("Task #%d is due.", one.id)
		}
		result, err := notify.Send(ctx, d.Write, notify.Message{
			Channel: notify.Assistant, Title: "atrium: reminder: " + one.title, Body: body,
			Now: func() float64 { return now },
		})
		if err != nil {
			return sent, err
		}
		if !result.Delivered {
			continue
		}
		if _, err := d.Write.ExecContext(context.WithoutCancel(ctx), "UPDATE tasks SET reminded_ts = ? WHERE id = ? AND reminded_ts IS NULL", now, one.id); err != nil {
			return sent, err
		}
		sent++
	}
	return sent, nil
}

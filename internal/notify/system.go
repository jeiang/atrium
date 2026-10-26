package notify

import (
	"context"
	"fmt"
	"time"

	"github.com/jeiang/atrium/internal/assistant/budget"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/py"
)

func PauseChanged(ctx context.Context, d *db.DB, env config.Env, paused bool) error {
	if on, err := Enabled(ctx, d.Read); err != nil || !on {
		return err
	}
	title := "atrium: mail writes resumed"
	if paused {
		title = "atrium: mail writes paused"
	}
	_, err := Send(ctx, d.Write, env, Message{Channel: System, Priority: 4, Title: title, Body: "Changed by the operator."})
	return err
}

func noticeBudget(ctx context.Context, d *db.DB, env config.Env, now float64) error {
	loc, err := config.Location(env)
	if err != nil {
		return err
	}
	status, err := budget.StatusAt(ctx, d.Read, now, loc)
	if err != nil {
		return err
	}
	t := time.Unix(int64(now), 0).In(loc)
	monthStart := float64(time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, loc).Unix())
	for _, threshold := range []float64{0.8, 1} {
		if status.Share < threshold {
			continue
		}
		title := fmt.Sprintf("atrium: %s of the monthly budget used", py.FormatPercent(threshold, 0))
		var n int
		if err := d.Read.QueryRowContext(ctx, "SELECT COUNT(*) FROM notifications WHERE channel = ? AND title = ? AND ts >= ?", System, title, monthStart).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			continue
		}
		body := fmt.Sprintf("$%s of $%s spent this month.", py.FormatFixed(status.SpentUSD, 2), py.FormatFixed(status.BudgetUSD, 2))
		if _, err := Send(ctx, d.Write, env, Message{Channel: System, Title: title, Body: body, Now: func() float64 { return now }}); err != nil {
			return err
		}
	}
	return nil
}

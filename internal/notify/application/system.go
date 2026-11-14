package application

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/connections"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/notify"
)

func ConnectionNotice() connections.Notifier {
	return func(ctx context.Context, d *db.DB, title, body string) error {
		if !notify.Enabled(ctx) {
			return nil
		}
		_, err := New(d).Send(ctx, SendInput{Message: notify.Message{Channel: notify.System, Priority: 4, Title: title, Body: body}})
		return err
	}
}

func noticeBudget(ctx context.Context, d *db.DB, env config.Env, now float64, budget notify.BudgetStatus) error {
	if budget == nil {
		return nil
	}
	loc, err := config.From(ctx).Location(env)
	if err != nil {
		return err
	}
	status, err := budget(ctx, d.Read, now, loc)
	if err != nil {
		return err
	}
	t := time.Unix(int64(now), 0).In(loc)
	monthStart := float64(time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, loc).Unix())
	for _, threshold := range []float64{0.8, 1} {
		if status.Share < threshold {
			continue
		}
		title := fmt.Sprintf("atrium: %s of the monthly budget used", strconv.FormatFloat(threshold*100, 'f', 0, 64)+"%")
		var n int
		if err := d.Read.QueryRowContext(ctx, "SELECT COUNT(*) FROM notifications WHERE channel = ? AND title = ? AND ts >= ?", notify.System, title, monthStart).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			continue
		}
		body := fmt.Sprintf("$%s of $%s spent this month; $%s committed, $%s reserved, $%s unknown, $%s expired estimate. Chat pauses at $%s.",
			strconv.FormatFloat(status.SpentUSD, 'f', 2, 64), strconv.FormatFloat(status.BudgetUSD, 'f', 2, 64),
			strconv.FormatFloat(status.CommittedUSD, 'f', 2, 64), strconv.FormatFloat(status.ReservedUSD, 'f', 2, 64),
			strconv.FormatFloat(status.UnknownUSD, 'f', 2, 64), strconv.FormatFloat(status.ExpiredUSD, 'f', 2, 64),
			strconv.FormatFloat(status.InteractiveCeilingUSD, 'f', 2, 64))
		if _, err := New(d).Send(ctx, SendInput{Message: notify.Message{Channel: notify.System, Title: title, Body: body, Now: func() float64 { return now },
			Occurrence: fmt.Sprintf("budget:%g:%g", monthStart, threshold)}}); err != nil {
			return err
		}
	}
	return nil
}

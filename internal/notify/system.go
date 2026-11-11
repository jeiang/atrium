package notify

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/integrations"
)

func init() {
	integrations.SetNotifier(func(ctx context.Context, d *db.DB, env config.Env, title, body string) error {
		if !Enabled(ctx) {
			return nil
		}
		_, err := Send(ctx, d, Message{Channel: System, Priority: 4, Title: title, Body: body})
		return err
	})
}

func PauseChanged(ctx context.Context, d *db.DB, env config.Env, paused bool) error {
	if !Enabled(ctx) {
		return nil
	}
	title := "atrium: mail writes resumed"
	if paused {
		title = "atrium: mail writes paused"
	}
	_, err := Send(ctx, d, Message{Channel: System, Priority: 4, Title: title, Body: "Changed by the operator."})
	return err
}

type Budget struct {
	SpentUSD  float64
	BudgetUSD float64
	Share     float64
}

type BudgetStatus func(ctx context.Context, q db.Querier, now float64, loc *time.Location) (Budget, error)

func noticeBudget(ctx context.Context, d *db.DB, env config.Env, now float64, budget BudgetStatus) error {
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
		if err := d.Read.QueryRowContext(ctx, "SELECT COUNT(*) FROM notifications WHERE channel = ? AND title = ? AND ts >= ?", System, title, monthStart).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			continue
		}
		body := fmt.Sprintf("$%s of $%s spent this month.", strconv.FormatFloat(status.SpentUSD, 'f', 2, 64), strconv.FormatFloat(status.BudgetUSD, 'f', 2, 64))
		if _, err := Send(ctx, d, Message{Channel: System, Title: title, Body: body, Now: func() float64 { return now },
			Occurrence: fmt.Sprintf("budget:%g:%g", monthStart, threshold)}); err != nil {
			return err
		}
	}
	return nil
}

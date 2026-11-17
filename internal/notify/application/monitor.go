package application

import (
	"context"
	"fmt"
	"time"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/notify"
	"github.com/jeiang/atrium/internal/passrun"
	"github.com/jeiang/atrium/internal/scheduler"
)

const staleEvery = 5 * time.Minute

var staleLog = logx.Logger("atrium.notify.stale")

func Monitor(ctx context.Context, d *db.DB, env config.Env, budget notify.BudgetStatus) error {
	warn := func(msg string, err error) {
		if err != nil && ctx.Err() == nil {
			staleLog.Warn(msg, logx.Err, err.Error())
		}
	}
	t := time.NewTicker(staleEvery)
	defer t.Stop()
	for ctx.Err() == nil {
		if notify.Enabled(ctx) {
			warn("stale pass check failed", noticeStale(ctx, d))
			warn("budget threshold check failed", noticeBudget(ctx, d, env, stamp(), budget))
		}
		select {
		case <-ctx.Done():
		case <-t.C:
		}
	}
	return ctx.Err()
}

func noticeStale(ctx context.Context, d *db.DB) error {
	latest, err := passrun.Latest(ctx, d.Read)
	if err != nil {
		return err
	}
	at := float64(time.Now().UnixNano()) / 1e9
	for _, l := range latest {
		stale, why := l.Stale(at)
		if !stale || l.Failed && scheduler.CauseReported(d, l.JobID, l.RunID) {
			continue
		}
		since := 0.0
		if l.Success != nil {
			since = *l.Success
		}
		title := "atrium: " + l.Job + " is stale"
		var n int
		if err := d.Read.QueryRowContext(ctx, "SELECT COUNT(*) FROM notifications WHERE channel = ? AND title = ? AND ts >= ?", notify.System, title, since).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			continue
		}
		if _, err := New(d).Send(ctx, SendInput{Message: notify.Message{Channel: notify.System, Title: title, Body: why,
			Occurrence: fmt.Sprintf("stale:%s:%g", l.Job, since)}}); err != nil {
			return err
		}
	}
	return nil
}

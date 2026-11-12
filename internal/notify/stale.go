package notify

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/passrun"
)

const staleEvery = 5 * time.Minute

var staleLog = logx.Logger("atrium.notify.stale")

func watchStale(base context.Context, d *db.DB, env config.Env, stopping <-chan struct{}, budget BudgetStatus) {
	ctx, cancel := context.WithCancel(base)
	defer cancel()
	go func() {
		<-stopping
		cancel()
	}()
	warn := func(msg string, err error) {
		if err != nil && ctx.Err() == nil {
			staleLog.Warn(msg, logx.Err, err.Error())
		}
	}
	t := time.NewTicker(staleEvery)
	defer t.Stop()
	for ctx.Err() == nil {
		if Enabled(ctx) {
			warn("stale pass check failed", noticeStale(ctx, d, env))
			warn("budget threshold check failed", noticeBudget(ctx, d, env, stamp(), budget))
		}
		select {
		case <-ctx.Done():
		case <-t.C:
		}
	}
}

func noticeStale(ctx context.Context, d *db.DB, env config.Env) error {
	stale, err := passrun.StaleJobs(ctx, d.Read)
	if err != nil {
		return err
	}
	fresh := map[string]struct {
		why string
		at  float64
	}{}
	err = d.WithTx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT job FROM pass_run_alerts")
		if err != nil {
			return err
		}
		alerted, err := db.Collect(rows, func(r *sql.Rows) (string, error) {
			var job string
			return job, r.Scan(&job)
		})
		if err != nil {
			return err
		}
		for _, job := range alerted {
			if _, still := stale[job]; still {
				delete(stale, job)
				continue
			}
			if _, err := tx.ExecContext(ctx, "DELETE FROM pass_run_alerts WHERE job = ?", job); err != nil {
				return err
			}
		}
		for job, why := range stale {
			at := float64(time.Now().UnixNano()) / 1e9
			if _, err := tx.ExecContext(ctx, "INSERT INTO pass_run_alerts (job, notified_at) VALUES (?, ?)", job, at); err != nil {
				return err
			}
			fresh[job] = struct {
				why string
				at  float64
			}{why, at}
		}
		return nil
	})
	if err != nil {
		return err
	}
	for job, alert := range fresh {
		if _, err := Send(ctx, d, Message{Channel: System, Title: "atrium: " + job + " is stale", Body: alert.why,
			Occurrence: fmt.Sprintf("stale:%s:%g", job, alert.at)}); err != nil {
			return err
		}
	}
	return nil
}

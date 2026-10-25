package notify

import (
	"context"
	"database/sql"
	"time"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/passrun"
)

const staleEvery = 5 * time.Minute

var staleLog = logx.Logger("atrium.notify.stale")

func watchStale(d *db.DB, env config.Env, stopping <-chan struct{}) {
	t := time.NewTicker(staleEvery)
	defer t.Stop()
	for {
		if err := noticeStale(context.Background(), d, env); err != nil {
			staleLog.Warn("stale pass check failed", logx.Err, err.Error())
		}
		select {
		case <-stopping:
			return
		case <-t.C:
		}
	}
}

func noticeStale(ctx context.Context, d *db.DB, env config.Env) error {
	stale, err := passrun.StaleJobs(ctx, d.Read)
	if err != nil {
		return err
	}
	fresh := map[string]string{}
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
			if _, err := tx.ExecContext(ctx, "INSERT INTO pass_run_alerts (job, notified_at) VALUES (?, ?)", job, float64(time.Now().UnixNano())/1e9); err != nil {
				return err
			}
			fresh[job] = why
		}
		return nil
	})
	if err != nil {
		return err
	}
	for job, why := range fresh {
		if _, err := Send(ctx, d.Write, env, Message{Title: "atrium: " + job + " is stale", Body: why}); err != nil {
			return err
		}
	}
	return nil
}

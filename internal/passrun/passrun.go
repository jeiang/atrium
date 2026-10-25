package passrun

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/settings"
)

const (
	RetentionKey = "pass_run_retention_days"

	Sync       = "sync"
	NotionSync = "notion-sync"
	Scan       = "scan"
	Backup     = "backup"

	OK     = "ok"
	Failed = "failed"
)

var MaxAge = map[string]time.Duration{
	Sync:       time.Hour,
	NotionSync: time.Hour,
	Scan:       35 * 24 * time.Hour,
	Backup:     8 * 24 * time.Hour,
}

var log = logx.Logger("atrium.passrun")

func init() {
	settings.MustRegister(settings.Def{
		Key: RetentionKey, Module: "core", Type: settings.Int, Default: "90",
		Check: settings.Between(1, 3650), Class: settings.Advanced, Scopes: []settings.Scope{settings.Instance},
	})
}

func now() float64 { return float64(time.Now().UnixNano()) / 1e9 }

func Track(ctx context.Context, d *db.DB, job string, fn func() error) error {
	started := now()
	err := fn()
	Record(context.WithoutCancel(ctx), d, job, started, err)
	return err
}

func Record(ctx context.Context, d *db.DB, job string, started float64, failure error) {
	outcome, message := OK, ""
	if failure != nil {
		outcome, message = Failed, logx.Scrub(failure.Error())
	}
	err := d.WithTx(ctx, func(tx *sql.Tx) error {
		days, err := settings.Default.Resolve(ctx, tx, RetentionKey, 0)
		if err != nil {
			return err
		}
		n, err := days.Int()
		if err != nil {
			return err
		}
		finished := now()
		if _, err := tx.ExecContext(ctx, "INSERT INTO pass_runs (job, started_at, finished_at, outcome, message) VALUES (?, ?, ?, ?, ?)",
			job, started, finished, outcome, message); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "DELETE FROM pass_runs WHERE started_at < ?", finished-float64(n)*86400)
		return err
	})
	if err != nil {
		log.Warn("pass run not recorded", logx.Job, job, logx.Err, err.Error())
	}
}

type Last struct {
	Job      string
	Outcome  string
	Finished float64
	Message  string
	Success  *float64
}

func Latest(ctx context.Context, q db.Querier) ([]Last, error) {
	rows, err := q.QueryContext(ctx, `SELECT r.job, r.outcome, r.finished_at, r.message,
		(SELECT MAX(s.finished_at) FROM pass_runs s WHERE s.job = r.job AND s.outcome = 'ok')
		FROM pass_runs r WHERE r.id = (SELECT MAX(id) FROM pass_runs WHERE job = r.job) ORDER BY r.job`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Last
	for rows.Next() {
		var l Last
		if err := rows.Scan(&l.Job, &l.Outcome, &l.Finished, &l.Message, &l.Success); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func (l Last) Stale(at float64) (bool, string) {
	if l.Outcome == Failed {
		return true, "last run failed: " + l.Message
	}
	limit, ok := MaxAge[l.Job]
	if ok && l.Success != nil && at-*l.Success > limit.Seconds() {
		return true, fmt.Sprintf("no success for %s", time.Duration((at-*l.Success)*float64(time.Second)).Round(time.Minute))
	}
	return false, ""
}

func StaleJobs(ctx context.Context, q db.Querier) (map[string]string, error) {
	latest, err := Latest(ctx, q)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	t := now()
	for _, l := range latest {
		if stale, why := l.Stale(t); stale {
			out[l.Job] = why
		}
	}
	return out, nil
}

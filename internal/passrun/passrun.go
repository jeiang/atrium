package passrun

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/scheduler"
	"github.com/jeiang/atrium/internal/unit"
)

const (
	Sync    = "sync"
	Scan    = "scan"
	Backup  = "backup"
	CalDAV  = "caldav"
	CardDAV = "carddav"
)

type pass struct {
	name   string
	maxAge time.Duration
}

var passes = map[string]pass{
	unit.Mail + "/" + Sync:        {Sync, time.Hour},
	unit.Mail + "/" + Scan:        {Scan, 35 * 24 * time.Hour},
	module.Core + "/" + Backup:    {Backup, 8 * 24 * time.Hour},
	unit.Calendar + "/" + CalDAV:  {CalDAV, time.Hour},
	unit.Contacts + "/" + CardDAV: {CardDAV, time.Hour},
}

func now() float64 { return float64(time.Now().UnixNano()) / 1e9 }

type Last struct {
	Job     string
	JobID   int64
	RunID   int64
	Failed  bool
	Message string
	Success *float64
	maxAge  time.Duration
}

func Latest(ctx context.Context, q db.Querier) ([]Last, error) {
	runs, err := scheduler.LastOutcomes(ctx, q)
	if err != nil {
		return nil, err
	}
	var out []Last
	for _, r := range runs {
		p, ok := passes[r.Key]
		if !ok {
			continue
		}
		out = append(out, Last{Job: p.name, JobID: r.JobID, RunID: r.RunID, Failed: r.Status == scheduler.StatusError, Message: logx.Scrub(r.Error), Success: r.OKEndedAt, maxAge: p.maxAge})
	}
	slices.SortFunc(out, func(a, b Last) int { return cmp.Compare(a.Job, b.Job) })
	return out, nil
}

func (l Last) Stale(at float64) (bool, string) {
	if l.Failed {
		return true, "last run failed: " + l.Message
	}
	if l.Success != nil && at-*l.Success > l.maxAge.Seconds() {
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

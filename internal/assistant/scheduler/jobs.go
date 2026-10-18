package scheduler

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/db/readpaths"
)

const (
	Operator   = "operator"
	Agent      = "agent"
	JobCron    = "cron"
	JobNightly = "nightly"
	JobBuiltin = "builtin"
)

type Querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

type Job struct {
	ID              int64
	Name            string
	Prompt          string
	Schedule        string
	TZ              string
	Effort          string
	Kind            string
	Enabled         bool
	Cancelled       bool
	NextDue         *float64
	LeaseOwner      *string
	LeaseUntil      *float64
	CreatedBy       string
	CreatedInThread *int64
	CreatedAt       float64
	LastRunID       *int64
	PromptBy        *string
}

type ListedJob struct {
	Job
	LastStatus   *string
	LastThreadID *int64
	LastStarted  *float64
	LastError    *string
}

const jobColumns = "id, name, prompt, schedule, tz, effort, kind, enabled, cancelled, next_due, lease_owner, lease_until, " +
	"created_by, created_in_thread, created_at, last_run_id, prompt_by"

func (j *Job) fields() []any {
	return []any{&j.ID, &j.Name, &j.Prompt, &j.Schedule, &j.TZ, &j.Effort, &j.Kind, &j.Enabled, &j.Cancelled, &j.NextDue,
		&j.LeaseOwner, &j.LeaseUntil, &j.CreatedBy, &j.CreatedInThread, &j.CreatedAt, &j.LastRunID, &j.PromptBy}
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func pyStrip(s string) string { return strings.TrimFunc(s, pyIsSpace) }

func errNoActive(id int64) error { return Error(fmt.Sprintf("no active schedule %d", id)) }

func errTooMany() error {
	return Error(fmt.Sprintf("at most %d active schedules; cancel one first", MaxActiveJobs))
}

func GetJob(ctx context.Context, q Querier, id int64) (*Job, error) {
	var j Job
	err := q.QueryRowContext(ctx, "SELECT "+jobColumns+" FROM jobs WHERE id = ?", id).Scan(j.fields()...)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &j, nil
}

func ActiveCount(ctx context.Context, q Querier) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, "SELECT COUNT(*) AS n FROM jobs WHERE enabled = 1 AND cancelled = 0 AND kind = ?", JobCron).Scan(&n)
	return n, err
}

func atCap(ctx context.Context, q Querier) error {
	n, err := ActiveCount(ctx, q)
	if err != nil {
		return err
	}
	if n >= MaxActiveJobs {
		return errTooMany()
	}
	return nil
}

type NewJob struct {
	Name       string
	Prompt     string
	Schedule   string
	TZ         string
	Now        float64
	CreatedBy  string
	ThreadID   *int64
	Kind       string
	SkipLimits bool
	PromptBy   string
}

func CreateJob(ctx context.Context, q Querier, n NewJob) (int64, error) {
	name, prompt := pyStrip(n.Name), pyStrip(n.Prompt)
	if name == "" || prompt == "" {
		return 0, Error("name and prompt are required")
	}
	if n.CreatedBy == "" {
		n.CreatedBy = Operator
	}
	if n.Kind == "" {
		n.Kind = JobCron
	}
	_, due, err := Validate(n.Schedule, n.TZ, n.Now, !n.SkipLimits)
	if err != nil {
		return 0, err
	}
	if !n.SkipLimits && n.Kind == JobCron {
		if err := atCap(ctx, q); err != nil {
			return 0, err
		}
	}
	res, err := q.ExecContext(ctx,
		"INSERT INTO jobs (name, prompt, schedule, tz, kind, next_due, created_by, created_in_thread, created_at, prompt_by) "+
			"VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		name, prompt, pyStrip(n.Schedule), n.TZ, n.Kind, due, n.CreatedBy, n.ThreadID, n.Now, nullable(n.PromptBy))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

type JobUpdate struct {
	Name       *string
	Prompt     *string
	Schedule   *string
	Enabled    *bool
	SkipLimits bool
	PromptBy   string
}

func UpdateJob(ctx context.Context, q Querier, id int64, now float64, u JobUpdate) (*Job, error) {
	job, err := GetJob(ctx, q, id)
	if err != nil {
		return nil, err
	}
	if job == nil || job.Cancelled {
		return nil, errNoActive(id)
	}
	var cols []string
	var args []any
	set := func(col string, v any) {
		cols = append(cols, col+" = ?")
		args = append(args, v)
	}
	if u.Name != nil {
		name := pyStrip(*u.Name)
		if name == "" {
			return nil, Error("name is empty")
		}
		set("name", name)
	}
	if u.Prompt != nil {
		prompt := pyStrip(*u.Prompt)
		if prompt == "" {
			return nil, Error("prompt is empty")
		}
		set("prompt", prompt)
		set("prompt_by", nullable(u.PromptBy))
	}
	schedule := job.Schedule
	if u.Schedule != nil {
		schedule = *u.Schedule
	}
	willEnable := job.Enabled
	if u.Enabled != nil {
		willEnable = *u.Enabled
	}
	nextDue := job.NextDue
	setNextDue := false
	if u.Schedule != nil {
		_, due, err := Validate(*u.Schedule, job.TZ, now, !u.SkipLimits)
		if err != nil {
			return nil, err
		}
		set("schedule", pyStrip(*u.Schedule))
		if willEnable {
			nextDue = &due
		}
		setNextDue = true
	}
	if u.Enabled != nil {
		set("enabled", willEnable)
		if willEnable && !job.Enabled {
			if !u.SkipLimits && job.Kind == JobCron {
				if err := atCap(ctx, q); err != nil {
					return nil, err
				}
			}
			_, due, err := Validate(schedule, job.TZ, now, false)
			if err != nil {
				return nil, err
			}
			nextDue, setNextDue = &due, true
		}
	}
	if setNextDue {
		set("next_due", nextDue)
	}
	if len(cols) > 0 {
		if _, err := q.ExecContext(ctx, "UPDATE jobs SET "+strings.Join(cols, ", ")+" WHERE id = ?", append(args, id)...); err != nil {
			return nil, err
		}
	}
	return GetJob(ctx, q, id)
}

func CancelJob(ctx context.Context, q Querier, id int64) error {
	job, err := GetJob(ctx, q, id)
	if err != nil {
		return err
	}
	if job == nil || job.Cancelled {
		return errNoActive(id)
	}
	if job.Kind == JobNightly || job.Kind == JobBuiltin {
		return Error("built-in runs cannot be cancelled; disable them instead")
	}
	_, err = q.ExecContext(ctx, "UPDATE jobs SET cancelled = 1, enabled = 0, next_due = NULL WHERE id = ?", id)
	return err
}

func ListJobs(ctx context.Context, q Querier, includeCancelled bool) ([]ListedJob, error) {
	where := "WHERE j.cancelled = 0"
	if includeCancelled {
		where = ""
	}
	rows, err := q.QueryContext(ctx, "SELECT j."+strings.ReplaceAll(jobColumns, ", ", ", j.")+
		", r.status, r.thread_id, r.started_at, r.error "+
		"FROM jobs j LEFT JOIN job_runs r ON r.id = j.last_run_id "+where+" ORDER BY j.kind DESC, j.id")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []ListedJob
	for rows.Next() {
		var j ListedJob
		if err := rows.Scan(append(j.fields(), &j.LastStatus, &j.LastThreadID, &j.LastStarted, &j.LastError)...); err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

func bit(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (j ListedJob) row() map[string]any {
	return map[string]any{
		"id": j.ID, "name": j.Name, "prompt": j.Prompt, "schedule": j.Schedule, "tz": j.TZ, "effort": j.Effort, "kind": j.Kind,
		"enabled": bit(j.Enabled), "cancelled": bit(j.Cancelled), "next_due": j.NextDue, "lease_owner": j.LeaseOwner,
		"lease_until": j.LeaseUntil, "created_by": j.CreatedBy, "created_in_thread": j.CreatedInThread,
		"created_at": j.CreatedAt, "last_run_id": j.LastRunID, "prompt_by": j.PromptBy, "last_status": j.LastStatus,
		"last_thread_id": j.LastThreadID, "last_started": j.LastStarted, "last_error": j.LastError,
	}
}

func init() {
	readpaths.Register(readpaths.Path{
		Name:   "scheduler.list_jobs",
		Golden: "scheduler.list_jobs",
		Run: func(ctx context.Context, d *db.DB) (any, error) {
			jobs, err := ListJobs(ctx, d.Read, true)
			if err != nil {
				return nil, err
			}
			rows := make([]map[string]any, len(jobs))
			for i, j := range jobs {
				rows[i] = j.row()
			}
			return rows, nil
		},
	})
}

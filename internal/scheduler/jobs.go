package scheduler

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/jeiang/atrium/internal/db"
)

const (
	Operator   = "operator"
	Agent      = "agent"
	JobCron    = "cron"
	JobNightly = "nightly"
	JobBuiltin = "builtin"
	cronOwner  = "assistant"
)

type State string

const (
	StateActive    State = "active"
	StateDisabled  State = "disabled"
	StateCancelled State = "cancelled"
)

func (s State) Active() bool { return s == StateActive }

type Job struct {
	ID              int64
	Name            string
	Prompt          string
	Schedule        string
	TZ              string
	Kind            string
	State           State
	NextDue         *float64
	LeaseOwner      *string
	LeaseUntil      *float64
	CreatedBy       string
	CreatedInThread *int64
	CreatedAt       float64
	LastRunID       *int64
	PromptBy        *string
	Module          string
	DeclarationKey  *string
	ActivatedAt     float64
}

func (j Job) Fixed() bool { return j.Kind == JobNightly || j.Kind == JobBuiltin }

func (j Job) AgentEditable() bool { return !j.Fixed() }

type ListedJob struct {
	Job
	LastStatus   *string
	LastThreadID *int64
	LastStarted  *float64
	LastError    *string
}

const jobColumns = "id, name, prompt, schedule, tz, kind, state, next_due, lease_owner, lease_until, " +
	"created_by, created_in_thread, created_at, last_run_id, prompt_by, module, declaration_key, activated_at"

func (j *Job) fields() []any {
	return []any{&j.ID, &j.Name, &j.Prompt, &j.Schedule, &j.TZ, &j.Kind, &j.State, &j.NextDue,
		&j.LeaseOwner, &j.LeaseUntil, &j.CreatedBy, &j.CreatedInThread, &j.CreatedAt, &j.LastRunID, &j.PromptBy,
		&j.Module, &j.DeclarationKey, &j.ActivatedAt}
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func errNoActive(id int64) error { return Error(fmt.Sprintf("no active schedule %d", id)) }

func errTooMany(limit int) error {
	return Error(fmt.Sprintf("at most %d active schedules; cancel one first", limit))
}

func GetJob(ctx context.Context, q db.Querier, id int64) (*Job, error) {
	var j Job
	err := q.QueryRowContext(ctx, "SELECT "+jobColumns+" FROM jobs WHERE id = ? AND state != ?", id, StateCancelled).Scan(j.fields()...)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &j, nil
}

func ActiveCount(ctx context.Context, q db.Querier) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, "SELECT COUNT(*) AS n FROM jobs WHERE state = ? AND kind = ?", StateActive, JobCron).Scan(&n)
	return n, err
}

func atCap(ctx context.Context, q db.Querier) error {
	n, err := ActiveCount(ctx, q)
	if err != nil {
		return err
	}
	limit := TuningAgentScheduleMaxActiveJobs
	if n >= limit {
		return errTooMany(limit)
	}
	return nil
}

type NewJob struct {
	Name      string
	Prompt    string
	Schedule  string
	TZ        string
	Now       float64
	CreatedBy string
	ThreadID  *int64
	Kind      string
	PromptBy  string
	Module    string
	Key       string
}

func CreateJob(ctx context.Context, q db.Querier, n NewJob) (int64, error) {
	name, prompt := strings.TrimSpace(n.Name), strings.TrimSpace(n.Prompt)
	if name == "" || prompt == "" {
		return 0, Error("name and prompt are required")
	}
	if n.CreatedBy == "" {
		n.CreatedBy = Operator
	}
	if n.Kind == "" {
		n.Kind = JobCron
	}
	switch {
	case n.Kind == JobCron && (n.Key != "" || (n.Module != "" && n.Module != cronOwner)):
		return 0, Error("a cron schedule belongs to the assistant and has no declaration key")
	case n.Kind == JobCron:
		n.Module = cronOwner
	case n.Key == "" || n.Module == "":
		return 0, Error("a declared job needs its owner module and declaration key")
	case n.Kind == JobCron && isEvery(n.Schedule):
		return 0, Error(errUnrecognised)
	}
	bypass := n.CreatedBy == Operator
	_, due, err := Validate(ctx, q, n.Schedule, n.TZ, n.Now, !bypass)
	if err != nil {
		return 0, err
	}
	if !bypass && n.Kind == JobCron {
		if err := atCap(ctx, q); err != nil {
			return 0, err
		}
	}
	res, err := q.ExecContext(ctx,
		"INSERT INTO jobs (name, prompt, schedule, tz, kind, state, next_due, created_by, created_in_thread, created_at, prompt_by, module, declaration_key, activated_at) "+
			"VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		name, prompt, strings.TrimSpace(n.Schedule), n.TZ, n.Kind, StateActive, due, n.CreatedBy, n.ThreadID, n.Now, nullable(n.PromptBy),
		n.Module, nullable(n.Key), n.Now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

type JobUpdate struct {
	Name     *string
	Prompt   *string
	Schedule *string
	Enabled  *bool
	Actor    string
	PromptBy string
}

func UpdateJob(ctx context.Context, q db.Querier, id int64, now float64, u JobUpdate) (*Job, error) {
	job, err := GetJob(ctx, q, id)
	if err != nil {
		return nil, err
	}
	if job == nil {
		return nil, errNoActive(id)
	}
	var cols []string
	var args []any
	set := func(col string, v any) {
		cols = append(cols, col+" = ?")
		args = append(args, v)
	}
	if u.Name != nil {
		name := strings.TrimSpace(*u.Name)
		if name == "" {
			return nil, Error("name is empty")
		}
		set("name", name)
	}
	if u.Prompt != nil {
		prompt := strings.TrimSpace(*u.Prompt)
		if prompt == "" {
			return nil, Error("prompt is empty")
		}
		set("prompt", prompt)
		set("prompt_by", nullable(u.PromptBy))
	}
	schedule := job.Schedule
	if u.Schedule != nil {
		if isEvery(*u.Schedule) {
			return nil, Error(errUnrecognised)
		}
		schedule = *u.Schedule
	}
	state := job.State
	if u.Enabled != nil {
		if *u.Enabled {
			state = StateActive
		} else {
			state = StateDisabled
		}
	}
	bypass := u.Actor == Operator
	enabling := state == StateActive && job.State != StateActive
	disabling := u.Enabled != nil && !*u.Enabled
	needsDue := u.Schedule != nil || enabling || disabling
	var due float64
	if needsDue && state == StateActive {
		_, due, err = Validate(ctx, q, schedule, job.TZ, now, !bypass)
		if err != nil {
			return nil, err
		}
	}
	if u.Schedule != nil {
		set("schedule", strings.TrimSpace(*u.Schedule))
	}
	if u.Enabled != nil {
		set("state", state)
		if enabling {
			set("activated_at", now)
		}
		if enabling && !bypass && job.Kind == JobCron {
			if err := atCap(ctx, q); err != nil {
				return nil, err
			}
		}
	}
	if needsDue {
		if state == StateActive {
			set("next_due", due)
		} else {
			set("next_due", nil)
		}
	}
	if len(cols) > 0 {
		if _, err := q.ExecContext(ctx, "UPDATE jobs SET "+strings.Join(cols, ", ")+" WHERE id = ?", append(args, id)...); err != nil {
			return nil, err
		}
	}
	return GetJob(ctx, q, id)
}

func CancelJob(ctx context.Context, q db.Querier, id int64) error {
	job, err := GetJob(ctx, q, id)
	if err != nil {
		return err
	}
	if job == nil {
		return errNoActive(id)
	}
	if job.Fixed() {
		return Error("built-in runs cannot be cancelled; disable them instead")
	}
	_, err = q.ExecContext(ctx, "UPDATE jobs SET state = ?, next_due = NULL WHERE id = ?", StateCancelled, id)
	return err
}

func ListJobs(ctx context.Context, q db.Querier, includeCancelled bool) ([]ListedJob, error) {
	where := "WHERE j.state != ?"
	args := []any{StateCancelled}
	if includeCancelled {
		where = ""
		args = nil
	}
	rows, err := q.QueryContext(ctx, "SELECT j."+strings.ReplaceAll(jobColumns, ", ", ", j.")+
		", r.status, r.thread_id, r.started_at, r.error "+
		"FROM jobs j LEFT JOIN job_runs r ON r.id = j.last_run_id "+where+" ORDER BY j.id", args...)
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

package application

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/scheduler"
	"github.com/jeiang/atrium/internal/settings"
	"github.com/jeiang/atrium/internal/timex"
	"github.com/jeiang/atrium/internal/tool"
)

const (
	ScheduleCreateName  application.Name = "assistant.schedule.create"
	ScheduleUpdateName  application.Name = "assistant.schedule.update"
	ScheduleEnabledName application.Name = "assistant.schedule.enabled"
	ScheduleCancelName  application.Name = "assistant.schedule.cancel"
	ScheduleListName    application.Name = "assistant.schedule.list"
	ScheduleHistoryName application.Name = "assistant.schedule.history"
	ScheduleAddName     application.Name = "assistant.schedule.add"
	ScheduleRunNowName  application.Name = "assistant.schedule.run_now"

	SourceBuiltin = "built-in"

	ScheduleDeleteOp  = "schedule_delete"
	ScheduleRestoreOp = "schedule_restore"

	scheduleStampLayout  = "Mon 02 Jan 15:04"
	scheduleDueLayout    = "2006-01-02T15:04-07:00"
	builtinAgentRefusal  = "built-in runs are managed by the operator"
	operatorAgentRefusal = "schedules the operator made are managed by the operator"
)

var (
	scheduleScopes    = []string{state.Jobs}
	agentListScopes   = []string{state.Jobs, state.Assistant}
	agentScheduleAuth = []application.Authority{application.Agent, application.Approved}
	anyScheduleAuth   = []application.Authority{application.Operator, application.Agent, application.Approved}
)

type ScheduleSource struct {
	ThreadID   int64
	RunID      int64
	ProposalID *int64
}

type ScheduleCreateInput struct {
	Source   ScheduleSource
	Name     string
	Prompt   string
	Schedule string
}

type ScheduleUpdateInput struct {
	ID       int64
	Agent    *ScheduleSource
	Name     *string
	Prompt   *string
	Schedule *string
	Enabled  *bool
	IfMatch  string
}

type ScheduleAddInput struct {
	Name     string
	Prompt   string
	Schedule string
}

type ScheduleRunNowInput struct {
	ID int64
}

type ScheduleRunStarted struct {
	Run    int64  `json:"run"`
	Job    int64  `json:"job"`
	Status string `json:"status"`
}

type ScheduleToggleInput struct {
	ID      int64
	Enabled *bool
}

type ScheduleCancelInput struct {
	ID    int64
	Agent *ScheduleSource
}

type ScheduleListInput struct {
	Editable bool
}

type ScheduleHistoryInput struct {
	JobID  int64
	Before int64
	Limit  int
}

type ScheduleJob struct {
	ID              int64   `json:"id"`
	Name            string  `json:"name"`
	Kind            string  `json:"kind"`
	CreatedBy       string  `json:"created_by"`
	Source          string  `json:"source"`
	Protected       bool    `json:"protected"`
	Revision        string  `json:"revision"`
	Cadence         string  `json:"cadence"`
	Schedule        string  `json:"schedule"`
	Prompt          string  `json:"prompt"`
	Enabled         bool    `json:"enabled"`
	NextDueText     *string `json:"next_due_text"`
	NextRunAt       *string `json:"next_run_at"`
	LastRunID       *int64  `json:"last_run_id"`
	LastStatus      *string `json:"last_status"`
	LastError       *string `json:"last_error"`
	LastStartedText *string `json:"last_started_text"`
	LastRunAt       *string `json:"last_run_at"`
	NextDue         *string `json:"-"`
	Fixed           bool    `json:"-"`
}

type ScheduleRun struct {
	ID          int64   `json:"id"`
	JobName     string  `json:"job_name"`
	Status      string  `json:"status"`
	StartedText *string `json:"started_text"`
	Cost        float64 `json:"cost"`
	Error       *string `json:"error"`
	ThreadID    *int64  `json:"thread_id"`
	Hint        string  `json:"-"`
}

type ScheduleList struct {
	Jobs  []ScheduleJob `json:"jobs"`
	TZ    string        `json:"tz"`
	Marks tool.Marks    `json:"-"`
}

type ScheduleChanged struct {
	ID       int64        `json:"id"`
	Enabled  bool         `json:"enabled"`
	Job      *ScheduleJob `json:"job,omitempty"`
	EffectID int64        `json:"effect_id,omitempty"`
}

type scheduleDeleteUndo struct {
	Op string `json:"op"`
	ID int64  `json:"id"`
}

type scheduleRestoreUndo struct {
	Op       string   `json:"op"`
	ID       int64    `json:"id"`
	Name     string   `json:"name"`
	Prompt   string   `json:"prompt"`
	Schedule string   `json:"schedule"`
	State    string   `json:"state"`
	NextDue  *float64 `json:"next_due"`
}

func DescribeScheduleCreate() application.Definition {
	return application.Describe[ScheduleCreateInput, ScheduleChanged](ScheduleCreateName, application.Command, agentScheduleAuth, application.LocalUndoable, true)
}

func DescribeScheduleUpdate() application.Definition {
	return application.Describe[ScheduleUpdateInput, ScheduleChanged](ScheduleUpdateName, application.Command, anyScheduleAuth, application.LocalUndoable, true)
}

func DescribeScheduleEnabled() application.Definition {
	return application.Describe[ScheduleToggleInput, ScheduleChanged](ScheduleEnabledName, application.Command, []application.Authority{application.Operator}, application.NoEffect, true)
}

func DescribeScheduleCancel() application.Definition {
	return application.Describe[ScheduleCancelInput, ScheduleChanged](ScheduleCancelName, application.Command, anyScheduleAuth, application.LocalUndoable, true)
}

func DescribeScheduleList() application.Definition {
	return application.Describe[ScheduleListInput, ScheduleList](ScheduleListName, application.Query, anyScheduleAuth, application.NoEffect, false)
}

func DescribeScheduleHistory() application.Definition {
	return describeRead[ScheduleHistoryInput, []ScheduleRun](ScheduleHistoryName)
}

func DescribeScheduleAdd() application.Definition {
	return application.Describe[ScheduleAddInput, ScheduleChanged](ScheduleAddName, application.Command, []application.Authority{application.Operator}, application.NoEffect, true)
}

func DescribeScheduleRunNow() application.Definition {
	return application.Describe[ScheduleRunNowInput, ScheduleRunStarted](ScheduleRunNowName, application.Command, []application.Authority{application.Operator}, application.NoEffect, true)
}

func ScheduleRevision(j scheduler.Job) string {
	sum := sha256.Sum256(fmt.Appendf(nil, "%d\x00%s\x00%s\x00%s\x00%s", j.ID, j.Name, j.Prompt, j.Schedule, j.State))
	return hex.EncodeToString(sum[:8])
}

func revisionMatches(ifMatch, revision string) bool {
	if strings.TrimSpace(ifMatch) == "" {
		return true
	}
	for tag := range strings.SplitSeq(ifMatch, ",") {
		tag = strings.TrimSpace(tag)
		if tag == "*" || strings.Trim(strings.TrimPrefix(tag, "W/"), `"`) == revision {
			return true
		}
	}
	return false
}

func scheduleInstant(epoch *float64, loc *time.Location) *string {
	if epoch == nil || *epoch == 0 {
		return nil
	}
	sec, us := timex.SplitTimestamp(*epoch)
	text := time.Unix(sec, us*1000).In(loc).Format(time.RFC3339)
	return &text
}

func (s *Service) WithJobs(defs []scheduler.Definition) *Service {
	c := *s
	c.jobs = defs
	return &c
}

func scheduleStamp(epoch *float64, loc *time.Location) *string {
	if epoch == nil || *epoch == 0 {
		return nil
	}
	sec, us := timex.SplitTimestamp(*epoch)
	text := time.Unix(sec, us*1000).In(loc).Format(scheduleStampLayout)
	return &text
}

func scheduleJobOf(j scheduler.ListedJob, zone *time.Location, agent bool) (ScheduleJob, error) {
	v := ScheduleJob{
		ID: j.ID, Name: j.Name, Kind: j.Kind, CreatedBy: j.CreatedBy, Source: j.CreatedBy, Revision: ScheduleRevision(j.Job),
		Cadence: scheduler.Describe(j.Schedule, j.TZ), Schedule: j.Schedule, Prompt: j.Prompt, Enabled: j.State.Active(),
		LastRunID: j.LastRunID, LastStatus: j.LastStatus, LastError: j.LastError, LastStartedText: scheduleStamp(j.LastStarted, zone),
		LastRunAt: scheduleInstant(j.LastStarted, zone),
	}
	if j.Fixed() {
		v.Source = SourceBuiltin
	}
	if j.State.Active() {
		v.NextDueText = scheduleStamp(j.NextDue, zone)
		v.NextRunAt = scheduleInstant(j.NextDue, zone)
	}
	if agent && j.NextDue != nil {
		loc, err := scheduler.Zone(j.TZ)
		if err != nil {
			return ScheduleJob{}, refusal("invalid time zone")
		}
		sec, us := timex.SplitTimestamp(*j.NextDue)
		due := time.Unix(sec, us*1000).In(loc).Format(scheduleDueLayout)
		v.NextDue = &due
	}
	return v, nil
}

func scheduleRunOf(run scheduler.ListedRun, zone *time.Location) ScheduleRun {
	at := run.DueAt
	if run.StartedAt != nil && *run.StartedAt != 0 {
		at = *run.StartedAt
	}
	hint := run.Status
	if strings.HasPrefix(hint, "skipped_") && (run.Tokens > 0 || run.Cost > 0) {
		hint += "_partial"
	}
	return ScheduleRun{ID: run.ID, JobName: run.JobName, Status: run.Status, StartedText: scheduleStamp(&at, zone), Cost: run.Cost, Error: run.Error, ThreadID: run.ThreadID, Hint: hint}
}

func (s *Service) scheduleZone(ctx context.Context) (string, *time.Location, error) {
	tz := config.From(ctx).TZName(s.env)
	zone, err := scheduler.Zone(tz)
	return tz, zone, err
}

func (s *Service) ScheduleList(ctx context.Context, in ScheduleListInput) (application.Read[ScheduleList], error) {
	tz, zone, err := s.scheduleZone(ctx)
	if err != nil {
		return application.Read[ScheduleList]{}, err
	}
	out := ScheduleList{Jobs: []ScheduleJob{}, TZ: tz}
	scopes := scheduleScopes
	if in.Editable {
		scopes = agentListScopes
	}
	rev, err := s.snapshot(ctx, string(ScheduleListName), scopes, in, func(q db.Querier) error {
		jobs, err := scheduler.ListJobs(ctx, q, false)
		if err != nil {
			return err
		}
		targets := []string{}
		for _, j := range jobs {
			if in.Editable && (j.Fixed() || j.CreatedBy == scheduler.Operator) {
				continue
			}
			view, err := scheduleJobOf(j, zone, in.Editable)
			if err != nil {
				return err
			}
			def, declared := scheduler.Declared(s.jobs, j.Job)
			view.Fixed = declared && def.CarriedSetting == ""
			view.Protected = declared && def.Protected
			out.Jobs = append(out.Jobs, view)
			targets = append(targets, target("schedule", j.ID))
		}
		if !in.Editable {
			return nil
		}
		out.Marks, err = threads.MarksOf(ctx, q, targets)
		return err
	})
	return application.Read[ScheduleList]{Revision: rev, Data: out}, err
}

func (s *Service) ScheduleHistory(ctx context.Context, in ScheduleHistoryInput) (application.Read[[]ScheduleRun], error) {
	_, zone, err := s.scheduleZone(ctx)
	if err != nil {
		return application.Read[[]ScheduleRun]{}, err
	}
	limit := in.Limit
	if limit <= 0 {
		limit = config.TuningWebRunHistory
	}
	out := []ScheduleRun{}
	rev, err := s.snapshot(ctx, string(ScheduleHistoryName), scheduleScopes, in, func(q db.Querier) error {
		if in.JobID != 0 {
			exists, err := scheduler.JobExists(ctx, q, in.JobID)
			if err != nil {
				return err
			}
			if !exists {
				return notFound("Unknown schedule.")
			}
		}
		runs, err := scheduler.ListRuns(ctx, q, in.JobID, in.Before, limit)
		for _, run := range runs {
			out = append(out, scheduleRunOf(run, zone))
		}
		return err
	})
	return application.Read[[]ScheduleRun]{Revision: rev, Data: out}, err
}

func refusal(message string) error {
	return &application.Error{Code: application.Refused, Message: message}
}

func scheduleError(err error) error {
	var domain scheduler.Error
	if errors.As(err, &domain) {
		return &application.Error{Code: application.Refused, Message: domain.Error(), Cause: err}
	}
	var bad settings.Invalid
	var pinned *settings.PinnedError
	if errors.As(err, &bad) || errors.As(err, &pinned) {
		return &application.Error{Code: application.Invalid, Message: err.Error(), Cause: err,
			Fields: []application.Field{{Name: "schedule", Code: "invalid", Message: err.Error()}}}
	}
	return err
}

func noSchedule(id int64) error {
	return notFound(fmt.Sprintf("no active schedule %d", id))
}

func (src ScheduleSource) effect(operation string) ledger.New {
	threadID, runID := src.ThreadID, src.RunID
	return ledger.New{Origin: "agent", Operation: operation, OperationKey: operation + ":" + ledger.NewKey(),
		Target: "schedule:new", ThreadID: &threadID, RunID: &runID, ProposalID: src.ProposalID}
}

func (s *Service) scheduleCommand(ctx context.Context, name application.Name, id int64, src *ScheduleSource, operation string,
	apply func(context.Context, *sql.Tx) (ScheduleChanged, ledger.Result, error)) (application.Change[ScheduleChanged], error) {
	var out ScheduleChanged
	var auditID int64
	err := s.db.WithTx(ctx, func(tx *sql.Tx) error {
		details := ""
		if src == nil {
			changed, _, err := apply(ctx, tx)
			if err != nil {
				return err
			}
			out = changed
		} else {
			effect, err := s.ledger.ApplyLocalTx(ctx, tx, src.effect(operation), func(ctx context.Context, tx *sql.Tx) (ledger.Result, error) {
				changed, res, err := apply(ctx, tx)
				out = changed
				return res, err
			})
			if err != nil {
				return err
			}
			out.EffectID = effect.ID
			details = fmt.Sprintf(`{"effect_ids":[%d]}`, effect.ID)
		}
		id = out.ID
		var err error
		auditID, err = s.state.Audit(ctx, tx, state.Entry{Operation: string(name), Outcome: state.OK, Target: target("schedule", id), DetailsJSON: details})
		return err
	})
	if err != nil {
		return application.Change[ScheduleChanged]{}, s.reject(ctx, name, target("schedule", id), scheduleError(err))
	}
	change := application.Change[ScheduleChanged]{Data: out, AuditID: auditID}
	if out.EffectID != 0 {
		change.EffectIDs = []int64{out.EffectID}
	}
	return change, nil
}

func (s *Service) scheduleView(ctx context.Context, job *scheduler.Job) (*ScheduleJob, error) {
	_, zone, err := s.scheduleZone(ctx)
	if err != nil {
		return nil, err
	}
	view, err := scheduleJobOf(scheduler.ListedJob{Job: *job}, zone, true)
	def, declared := scheduler.Declared(s.jobs, *job)
	view.Fixed = declared && def.CarriedSetting == ""
	view.Protected = declared && def.Protected
	return &view, err
}

func (s *Service) existingJob(ctx context.Context, q db.Querier, id int64, agent bool) (*scheduler.Job, error) {
	job, err := scheduler.GetJob(ctx, q, id)
	if err != nil {
		return nil, err
	}
	if job == nil || job.State == scheduler.StateCancelled {
		return nil, noSchedule(id)
	}
	if agent && job.Fixed() {
		return nil, refusal(builtinAgentRefusal)
	}
	if agent && job.CreatedBy == scheduler.Operator {
		return nil, refusal(operatorAgentRefusal)
	}
	return job, nil
}

func (s *Service) protectedRefusal(job *scheduler.Job) error {
	if def, declared := scheduler.Declared(s.jobs, *job); declared && def.Protected {
		return refusal(job.Name + " is protected and cannot be disabled; disable its module or pause atrium instead")
	}
	return nil
}

func (s *Service) ScheduleAdd(ctx context.Context, in ScheduleAddInput) (application.Change[ScheduleChanged], error) {
	now := s.now()
	return s.scheduleCommand(ctx, ScheduleAddName, 0, nil, "", func(ctx context.Context, tx *sql.Tx) (ScheduleChanged, ledger.Result, error) {
		name, prompt, schedule := strings.TrimSpace(in.Name), strings.TrimSpace(in.Prompt), strings.TrimSpace(in.Schedule)
		switch {
		case name == "":
			return ScheduleChanged{}, ledger.Result{}, invalid("name", "name is empty", nil)
		case prompt == "":
			return ScheduleChanged{}, ledger.Result{}, invalid("prompt", "prompt is empty", nil)
		case schedule == "":
			return ScheduleChanged{}, ledger.Result{}, invalid("schedule", "schedule is empty", nil)
		}
		tz := config.From(ctx).TZName(s.env)
		if _, err := scheduler.Parse(schedule, tz); err != nil {
			return ScheduleChanged{}, ledger.Result{}, invalid("schedule", err.Error(), nil)
		}
		id, err := scheduler.CreateJob(ctx, tx, scheduler.NewJob{
			Name: name, Prompt: prompt, Schedule: schedule, TZ: tz, Now: now,
			CreatedBy: scheduler.Operator, PromptBy: scheduler.Operator, Kind: scheduler.JobCron, Floor: true,
		})
		if err != nil {
			return ScheduleChanged{}, ledger.Result{}, err
		}
		job, err := scheduler.GetJob(ctx, tx, id)
		if err != nil {
			return ScheduleChanged{}, ledger.Result{}, err
		}
		view, err := s.scheduleView(ctx, job)
		if err != nil {
			return ScheduleChanged{}, ledger.Result{}, err
		}
		return ScheduleChanged{ID: id, Enabled: job.State.Active(), Job: view}, ledger.Result{}, nil
	})
}

func (s *Service) ScheduleRunNow(ctx context.Context, in ScheduleRunNowInput) (application.Change[ScheduleRunStarted], error) {
	where := target("schedule", in.ID)
	job, err := s.existingJob(ctx, s.db.Read, in.ID, false)
	if err != nil {
		return application.Change[ScheduleRunStarted]{}, s.reject(ctx, ScheduleRunNowName, where, err)
	}
	running, err := db.ServeRunning(ctx, config.From(ctx).DBPath())
	if err != nil {
		return application.Change[ScheduleRunStarted]{}, err
	}
	if !running {
		return application.Change[ScheduleRunStarted]{}, &application.Error{Code: application.Unavailable, Message: "atrium serve is not running on this database; start it to run jobs"}
	}
	runID, err := scheduler.RequestJob(ctx, s.db, job.ID, s.now())
	if busy, ok := errors.AsType[*scheduler.Running](err); ok {
		return application.Change[ScheduleRunStarted]{}, s.reject(ctx, ScheduleRunNowName, where,
			&application.Error{Code: application.Conflict, Message: fmt.Sprintf("%s is already running (run %d)", job.Name, busy.RunID)})
	}
	if err != nil {
		return application.Change[ScheduleRunStarted]{}, s.reject(ctx, ScheduleRunNowName, where, scheduleError(err))
	}
	auditID, err := s.audit(ctx, nil, ScheduleRunNowName, where, state.OK)
	if err != nil {
		return application.Change[ScheduleRunStarted]{}, err
	}
	return application.Change[ScheduleRunStarted]{Data: ScheduleRunStarted{Run: runID, Job: job.ID, Status: string(scheduler.StatusRequested)}, AuditID: auditID}, nil
}

func restoreUndo(job *scheduler.Job) json.RawMessage {
	return jsonOf(scheduleRestoreUndo{Op: ScheduleRestoreOp, ID: job.ID, Name: job.Name, Prompt: job.Prompt, Schedule: job.Schedule,
		State: string(job.State), NextDue: job.NextDue})
}

func (s *Service) ScheduleCreate(ctx context.Context, in ScheduleCreateInput) (application.Change[ScheduleChanged], error) {
	now := s.now()
	return s.scheduleCommand(ctx, ScheduleCreateName, 0, &in.Source, "schedule_create", func(ctx context.Context, tx *sql.Tx) (ScheduleChanged, ledger.Result, error) {
		threadID := in.Source.ThreadID
		id, err := scheduler.CreateJob(ctx, tx, scheduler.NewJob{
			Name: in.Name, Prompt: in.Prompt, Schedule: in.Schedule, TZ: config.From(ctx).TZName(s.env), Now: now,
			CreatedBy: scheduler.Agent, PromptBy: scheduler.Agent, ThreadID: &threadID,
		})
		if err != nil {
			return ScheduleChanged{}, ledger.Result{}, err
		}
		job, err := scheduler.GetJob(ctx, tx, id)
		if err != nil {
			return ScheduleChanged{}, ledger.Result{}, err
		}
		view, err := s.scheduleView(ctx, job)
		if err != nil {
			return ScheduleChanged{}, ledger.Result{}, err
		}
		return ScheduleChanged{ID: id, Enabled: job.State.Active(), Job: view}, ledger.Result{
			State:  ledger.Done,
			Target: target("schedule", id),
			After:  jsonOf(map[string]any{"id": id, "name": in.Name, "schedule": in.Schedule}),
			Undo:   jsonOf(scheduleDeleteUndo{Op: ScheduleDeleteOp, ID: id}),
		}, nil
	})
}

func (s *Service) ScheduleUpdate(ctx context.Context, in ScheduleUpdateInput) (application.Change[ScheduleChanged], error) {
	now := s.now()
	if in.Agent == nil {
		return s.scheduleCommand(ctx, ScheduleUpdateName, in.ID, nil, "", func(ctx context.Context, tx *sql.Tx) (ScheduleChanged, ledger.Result, error) {
			job, err := s.existingJob(ctx, tx, in.ID, false)
			if err != nil {
				return ScheduleChanged{}, ledger.Result{}, err
			}
			if !revisionMatches(in.IfMatch, ScheduleRevision(*job)) {
				return ScheduleChanged{}, ledger.Result{}, &application.Error{Code: application.Precondition, Message: "The schedule changed since it was read; reload it and try again."}
			}
			def, known := scheduler.Declared(s.jobs, *job)
			u := scheduler.JobUpdate{Actor: scheduler.Operator, Floor: def.PromptRun || job.Kind == scheduler.JobCron}
			if in.Schedule != nil {
				schedule := strings.TrimSpace(*in.Schedule)
				if schedule == "" {
					return ScheduleChanged{}, ledger.Result{}, invalid("schedule", "schedule is empty", nil)
				}
				if _, err := scheduler.Parse(schedule, job.TZ); err != nil {
					return ScheduleChanged{}, ledger.Result{}, invalid("schedule", err.Error(), nil)
				}
				if known && def.CarriedSetting == "" {
					return ScheduleChanged{}, ledger.Result{}, refusal("the schedule of " + job.Name + " is fixed by its module")
				}
				u.Schedule = &schedule
			}
			if in.Name != nil {
				name := strings.TrimSpace(*in.Name)
				if name == "" {
					return ScheduleChanged{}, ledger.Result{}, invalid("name", "name is empty", nil)
				}
				if job.Fixed() {
					return ScheduleChanged{}, ledger.Result{}, refusal("the name of " + job.Name + " is fixed by its module")
				}
				u.Name = &name
			}
			if in.Prompt != nil {
				prompt := strings.TrimSpace(*in.Prompt)
				if prompt == "" {
					return ScheduleChanged{}, ledger.Result{}, invalid("prompt", "prompt is empty", nil)
				}
				if job.Fixed() {
					return ScheduleChanged{}, ledger.Result{}, refusal("the prompt of " + job.Name + " is fixed by its module")
				}
				u.Prompt, u.PromptBy = &prompt, scheduler.Operator
			}
			if in.Enabled != nil {
				if !*in.Enabled {
					if err := s.protectedRefusal(job); err != nil {
						return ScheduleChanged{}, ledger.Result{}, err
					}
				}
				u.Enabled = in.Enabled
			}
			if u.Schedule == nil && u.Name == nil && u.Prompt == nil && u.Enabled == nil {
				return ScheduleChanged{}, ledger.Result{}, invalid("schedule", "nothing to change", nil)
			}
			updated, err := scheduler.UpdateJob(ctx, tx, job.ID, now, u)
			if err != nil {
				return ScheduleChanged{}, ledger.Result{}, err
			}
			view, err := s.scheduleView(ctx, updated)
			if err != nil {
				return ScheduleChanged{}, ledger.Result{}, err
			}
			return ScheduleChanged{ID: job.ID, Enabled: updated.State.Active(), Job: view}, ledger.Result{}, nil
		})
	}
	return s.scheduleCommand(ctx, ScheduleUpdateName, in.ID, in.Agent, "schedule_update", func(ctx context.Context, tx *sql.Tx) (ScheduleChanged, ledger.Result, error) {
		job, err := s.existingJob(ctx, tx, in.ID, true)
		if err != nil {
			return ScheduleChanged{}, ledger.Result{}, err
		}
		updated, err := scheduler.UpdateJob(ctx, tx, job.ID, now, scheduler.JobUpdate{
			Name: in.Name, Prompt: in.Prompt, Schedule: in.Schedule, Enabled: in.Enabled, PromptBy: scheduler.Agent, Actor: scheduler.Agent,
		})
		if err != nil {
			return ScheduleChanged{}, ledger.Result{}, err
		}
		view, err := s.scheduleView(ctx, updated)
		if err != nil {
			return ScheduleChanged{}, ledger.Result{}, err
		}
		return ScheduleChanged{ID: job.ID, Enabled: updated.State.Active(), Job: view}, ledger.Result{
			State:  ledger.Done,
			Target: target("schedule", job.ID),
			Before: jsonOf(map[string]any{"name": job.Name, "prompt": job.Prompt, "schedule": job.Schedule, "enabled": job.State.Active()}),
			After:  jsonOf(map[string]any{"name": updated.Name, "prompt": updated.Prompt, "schedule": updated.Schedule, "enabled": updated.State.Active()}),
			Undo:   restoreUndo(job),
		}, nil
	})
}

func (s *Service) ScheduleEnabled(ctx context.Context, in ScheduleToggleInput) (application.Change[ScheduleChanged], error) {
	now := s.now()
	return s.scheduleCommand(ctx, ScheduleEnabledName, in.ID, nil, "", func(ctx context.Context, tx *sql.Tx) (ScheduleChanged, ledger.Result, error) {
		job, err := s.existingJob(ctx, tx, in.ID, false)
		if err != nil {
			return ScheduleChanged{}, ledger.Result{}, err
		}
		enabled := !job.State.Active()
		if in.Enabled != nil {
			enabled = *in.Enabled
		}
		if !enabled {
			if err := s.protectedRefusal(job); err != nil {
				return ScheduleChanged{}, ledger.Result{}, err
			}
		}
		if _, err := scheduler.UpdateJob(ctx, tx, job.ID, now, scheduler.JobUpdate{Enabled: &enabled, Actor: scheduler.Operator}); err != nil {
			return ScheduleChanged{}, ledger.Result{}, err
		}
		return ScheduleChanged{ID: job.ID, Enabled: enabled}, ledger.Result{}, nil
	})
}

func (s *Service) ScheduleCancel(ctx context.Context, in ScheduleCancelInput) (application.Change[ScheduleChanged], error) {
	return s.scheduleCommand(ctx, ScheduleCancelName, in.ID, in.Agent, "schedule_cancel", func(ctx context.Context, tx *sql.Tx) (ScheduleChanged, ledger.Result, error) {
		job, err := s.existingJob(ctx, tx, in.ID, in.Agent != nil)
		if err != nil {
			return ScheduleChanged{}, ledger.Result{}, err
		}
		if err := scheduler.CancelJob(ctx, tx, job.ID); err != nil {
			return ScheduleChanged{}, ledger.Result{}, err
		}
		return ScheduleChanged{ID: job.ID}, ledger.Result{
			State:  ledger.Done,
			Target: target("schedule", job.ID),
			Before: jsonOf(map[string]any{"enabled": true}),
			After:  jsonOf(map[string]any{"enabled": false}),
			Undo:   restoreUndo(job),
		}, nil
	})
}

func ScheduleUndo() []ledger.UndoDefinition {
	var out []ledger.UndoDefinition
	for _, name := range []string{ScheduleDeleteOp, ScheduleRestoreOp} {
		out = append(out, ledger.UndoDefinition{Operation: name, Undoer: ledger.Undoer{
			Validate: validateScheduleUndo(name),
			Local: func(ctx context.Context, tx *sql.Tx, raw json.RawMessage) (ledger.Result, error) {
				var value any
				var err error
				if name == ScheduleDeleteOp {
					value, err = undoScheduleDelete(ctx, tx, raw)
				} else {
					value, err = undoScheduleRestore(ctx, tx, raw)
				}
				if err != nil {
					return ledger.Result{}, err
				}
				return ledger.Result{State: ledger.Done, After: jsonOf(value)}, nil
			},
		}})
	}
	return out
}

func validateScheduleUndo(name string) func(json.RawMessage) error {
	return func(raw json.RawMessage) error {
		var head struct {
			Op string `json:"op"`
		}
		if err := json.Unmarshal(raw, &head); err != nil {
			return ledger.Refused("schedule undo payload is invalid")
		}
		if head.Op != name {
			return ledger.Refused("schedule undo payload names " + head.Op)
		}
		return nil
	}
}

func undoScheduleDelete(ctx context.Context, q db.Querier, raw json.RawMessage) (any, error) {
	var u scheduleDeleteUndo
	if err := json.Unmarshal(raw, &u); err != nil {
		return nil, ledger.Refused("schedule undo payload is invalid")
	}
	deleted, err := scheduler.DeleteAgentJob(ctx, q, u.ID)
	if err != nil {
		return nil, err
	}
	if !deleted {
		return nil, ledger.Refused("the schedule was changed or cancelled")
	}
	return map[string]any{"deleted": u.ID}, nil
}

func undoScheduleRestore(ctx context.Context, q db.Querier, raw json.RawMessage) (any, error) {
	var u scheduleRestoreUndo
	if err := json.Unmarshal(raw, &u); err != nil {
		return nil, ledger.Refused("schedule undo payload is invalid")
	}
	restored, err := scheduler.RestoreJob(ctx, q, scheduler.Restore{
		ID: u.ID, Name: u.Name, Prompt: u.Prompt, Schedule: u.Schedule, State: u.State, NextDue: u.NextDue,
	})
	if err != nil {
		return nil, err
	}
	if !restored {
		return nil, ledger.Refused("the schedule no longer exists")
	}
	return map[string]any{"restored": u.ID}, nil
}

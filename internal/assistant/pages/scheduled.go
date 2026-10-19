package pages

import (
	"database/sql"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jeiang/atrium/internal/assistant/scheduler"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/web"
	"github.com/jeiang/atrium/internal/web/tpl"
)

const (
	scheduleHistory    = 30
	scheduleJobHistory = 10
	stampLayout        = "Mon 02 Jan 15:04"
)

type JobView struct {
	ID              int64   `json:"id"`
	Name            string  `json:"name"`
	Kind            string  `json:"kind"`
	CreatedBy       string  `json:"created_by"`
	Cadence         string  `json:"cadence"`
	Schedule        string  `json:"schedule"`
	Prompt          string  `json:"prompt"`
	Enabled         bool    `json:"enabled"`
	NextDueText     *string `json:"next_due_text"`
	LastStatus      *string `json:"last_status"`
	LastError       *string `json:"last_error"`
	LastStartedText *string `json:"last_started_text"`
}

type RunView struct {
	JobName     string  `json:"job_name"`
	Status      string  `json:"status"`
	StartedText *string `json:"started_text"`
	Cost        float64 `json:"cost"`
	Error       *string `json:"error"`
	ThreadID    *int64  `json:"thread_id"`
}

type ScheduleFormValues struct {
	Name     string `json:"name"`
	Schedule string `json:"schedule"`
	Prompt   string `json:"prompt"`
}

type ScheduledData struct {
	Jobs []JobView `json:"jobs"`
	Runs []RunView `json:"runs"`
	TZ   string    `json:"tz"`
}

type ScheduleFormData struct {
	Job    *JobView           `json:"job"`
	Values ScheduleFormValues `json:"values"`
	Error  *string            `json:"error"`
	Runs   []RunView          `json:"runs"`
	TZ     string             `json:"tz"`
}

func stamp(epoch *float64, loc *time.Location) *string {
	if epoch == nil || *epoch == 0 {
		return nil
	}
	sec, us := py.SplitTimestamp(*epoch)
	text := time.Unix(sec, us*1000).In(loc).Format(stampLayout)
	return &text
}

func jobView(j scheduler.ListedJob, zone *time.Location) JobView {
	v := JobView{
		ID: j.ID, Name: j.Name, Kind: j.Kind, CreatedBy: j.CreatedBy, Cadence: scheduler.Describe(j.Schedule, j.TZ), Schedule: j.Schedule,
		Prompt: j.Prompt, Enabled: j.Enabled, LastStatus: j.LastStatus, LastError: j.LastError, LastStartedText: stamp(j.LastStarted, zone),
	}
	if j.Enabled {
		v.NextDueText = stamp(j.NextDue, zone)
	}
	return v
}

func formJob(j scheduler.Job) JobView {
	return JobView{ID: j.ID, Name: j.Name, Kind: j.Kind, Schedule: j.Schedule, Prompt: j.Prompt}
}

func runView(run scheduler.ListedRun, zone *time.Location) RunView {
	at := run.DueAt
	if run.StartedAt != nil && *run.StartedAt != 0 {
		at = *run.StartedAt
	}
	return RunView{JobName: run.JobName, Status: run.Status, StartedText: stamp(&at, zone), Cost: run.Cost, Error: run.Error, ThreadID: run.ThreadID}
}

func runViews(runs []scheduler.ListedRun, zone *time.Location) []RunView {
	out := make([]RunView, len(runs))
	for i, run := range runs {
		out[i] = runView(run, zone)
	}
	return out
}

func formValues(job *scheduler.Job, submitted *ScheduleFormValues) ScheduleFormValues {
	switch {
	case submitted != nil:
		return *submitted
	case job != nil:
		return ScheduleFormValues{Name: job.Name, Schedule: job.Schedule, Prompt: job.Prompt}
	}
	return ScheduleFormValues{}
}

func fixedJob(job *scheduler.Job) bool {
	return job.Kind == scheduler.JobNightly || job.Kind == scheduler.JobBuiltin
}

func (p *pages) scheduledRoutes(r chi.Router) {
	r.Get("/scheduled", p.scheduledIndex)
	r.Get("/scheduled/new", p.scheduleNewForm)
	r.Get("/scheduled/{job_id}/edit", p.scheduleEditForm)
	r.Post("/scheduled", p.scheduleCreate)
	r.Post("/scheduled/{job_id}", p.scheduleUpdate)
	r.Post("/scheduled/{job_id}/toggle", p.scheduleToggle)
	r.Post("/scheduled/{job_id}/cancel", p.scheduleCancel)
}

func (p *pages) scheduledData(r *http.Request) ScheduledData {
	ctx := detached(r)
	tz := config.TZName(p.deps.Env)
	now := p.now()
	check(p.deps.DB.WithTx(ctx, func(tx *sql.Tx) error {
		if err := scheduler.EnsureNightly(ctx, tx, tz, now); err != nil {
			return err
		}
		return scheduler.EnsureBuiltins(ctx, tx, tz, now)
	}))
	zone, err := scheduler.Zone(tz)
	check(err)
	jobs, err := scheduler.ListJobs(ctx, p.deps.DB.Read, false)
	check(err)
	runs, err := scheduler.ListRuns(ctx, p.deps.DB.Read, 0, scheduleHistory)
	check(err)
	views := make([]JobView, len(jobs))
	for i, j := range jobs {
		views[i] = jobView(j, zone)
	}
	return ScheduledData{Jobs: views, Runs: runViews(runs, zone), TZ: tz}
}

func (p *pages) scheduledShow(w http.ResponseWriter, r *http.Request, status int, toast *tpl.Toast, headers func(http.Header)) {
	data := p.scheduledData(r)
	if headers != nil {
		headers(w.Header())
	}
	if web.IsPartial(r) {
		p.renderFragment(w, r, status, "_scheduled_list.gohtml", data, toast)
		return
	}
	p.render(w, r, status, "scheduled", "scheduled.gohtml", data, toast)
}

func listHeaders(closeDialogs bool) func(http.Header) {
	return func(h http.Header) {
		h.Set("HX-Retarget", "#scheduled-list")
		h.Set("HX-Reswap", "outerHTML")
		if closeDialogs {
			h.Set("HX-Trigger", "close-dialogs")
		}
	}
}

func (p *pages) scheduleForm(w http.ResponseWriter, r *http.Request, status int, job *scheduler.Job, problem string, submitted *ScheduleFormValues) {
	ctx := r.Context()
	tz := config.TZName(p.deps.Env)
	var runs []scheduler.ListedRun
	if job != nil {
		found, err := scheduler.ListRuns(ctx, p.deps.DB.Read, job.ID, scheduleJobHistory)
		check(err)
		runs = found
	}
	zone, err := scheduler.Zone(tz)
	check(err)
	data := ScheduleFormData{Values: formValues(job, submitted), Runs: runViews(runs, zone), TZ: tz}
	if job != nil {
		view := formJob(*job)
		data.Job = &view
	}
	if problem != "" {
		data.Error = &problem
	}
	p.renderFragment(w, r, status, "_schedule_form.gohtml", data, nil)
}

func (p *pages) loadJob(w http.ResponseWriter, r *http.Request, jobID int64) (*scheduler.Job, bool) {
	job, err := scheduler.GetJob(r.Context(), p.deps.DB.Read, jobID)
	check(err)
	if job == nil || job.Cancelled {
		p.fail(w, r, http.StatusNotFound, "unknown schedule")
		return nil, false
	}
	return job, true
}

func (p *pages) attempted(w http.ResponseWriter, r *http.Request, err error, success string, job *scheduler.Job, submitted *ScheduleFormValues, inForm bool) {
	var refused scheduler.Error
	if errors.As(err, &refused) {
		failed := notice("error", "Not saved", err.Error())
		switch {
		case !web.IsPartial(r):
			p.scheduledShow(w, r, http.StatusUnprocessableEntity, failed, nil)
		case inForm:
			p.scheduleForm(w, r, http.StatusUnprocessableEntity, job, err.Error(), submitted)
		default:
			p.scheduledShow(w, r, http.StatusUnprocessableEntity, failed, listHeaders(false))
		}
		return
	}
	check(err)
	if !web.IsPartial(r) {
		redirect(w, r, "/scheduled")
		return
	}
	p.scheduledShow(w, r, http.StatusOK, notice("success", success, ""), listHeaders(true))
}

func (p *pages) scheduledIndex(w http.ResponseWriter, r *http.Request) {
	p.scheduledShow(w, r, http.StatusOK, nil, nil)
}

func (p *pages) scheduleNewForm(w http.ResponseWriter, r *http.Request) {
	p.scheduleForm(w, r, http.StatusOK, nil, "", nil)
}

func (p *pages) scheduleEditForm(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "job_id")
	if !ok {
		return
	}
	job, ok := p.loadJob(w, r, id)
	if !ok {
		return
	}
	p.scheduleForm(w, r, http.StatusOK, job, "", nil)
}

func submittedSchedule(r *http.Request) ScheduleFormValues {
	form := web.FormValues(r)
	return ScheduleFormValues{
		Name: web.FormString(form, "name", ""), Schedule: web.FormString(form, "schedule", ""), Prompt: web.FormString(form, "prompt", ""),
	}
}

func (p *pages) scheduleCreate(w http.ResponseWriter, r *http.Request) {
	values := submittedSchedule(r)
	_, err := scheduler.CreateJob(detached(r), p.deps.DB.Write, scheduler.NewJob{
		Name: values.Name, Prompt: values.Prompt, Schedule: values.Schedule, TZ: config.TZName(p.deps.Env), Now: p.now(),
		SkipLimits: true, PromptBy: scheduler.Operator,
	})
	p.attempted(w, r, err, "Schedule created", nil, &values, true)
}

func (p *pages) scheduleUpdate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "job_id")
	if !ok {
		return
	}
	values := submittedSchedule(r)
	job, ok := p.loadJob(w, r, id)
	if !ok {
		return
	}
	update := scheduler.JobUpdate{Schedule: &values.Schedule, SkipLimits: true, PromptBy: scheduler.Operator}
	if !fixedJob(job) {
		update.Name, update.Prompt = &values.Name, &values.Prompt
	}
	_, err := scheduler.UpdateJob(detached(r), p.deps.DB.Write, id, p.now(), update)
	p.attempted(w, r, err, "Schedule saved", job, &values, true)
}

func (p *pages) scheduleToggle(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "job_id")
	if !ok {
		return
	}
	job, ok := p.loadJob(w, r, id)
	if !ok {
		return
	}
	enabled := !job.Enabled
	_, err := scheduler.UpdateJob(detached(r), p.deps.DB.Write, id, p.now(), scheduler.JobUpdate{Enabled: &enabled, SkipLimits: true})
	p.attempted(w, r, err, "Schedule updated", nil, nil, false)
}

func (p *pages) scheduleCancel(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "job_id")
	if !ok {
		return
	}
	if _, ok := p.loadJob(w, r, id); !ok {
		return
	}
	p.attempted(w, r, scheduler.CancelJob(detached(r), p.deps.DB.Write, id), "Schedule deleted", nil, nil, false)
}

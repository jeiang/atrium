package pages

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/scheduler"
	"github.com/jeiang/atrium/internal/settings"
	"github.com/jeiang/atrium/internal/settings/audit"
	"github.com/jeiang/atrium/internal/timex"
	"github.com/jeiang/atrium/internal/web"
	"github.com/jeiang/atrium/internal/web/tpl"
)

const (
	stampLayout = "Mon 02 Jan 15:04"
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

type ScheduledData struct {
	Jobs []JobView `json:"jobs"`
	Runs []RunView `json:"runs"`
	TZ   string    `json:"tz"`
}

func stamp(epoch *float64, loc *time.Location) *string {
	if epoch == nil || *epoch == 0 {
		return nil
	}
	sec, us := timex.SplitTimestamp(*epoch)
	text := time.Unix(sec, us*1000).In(loc).Format(stampLayout)
	return &text
}

func jobView(j scheduler.ListedJob, zone *time.Location) JobView {
	v := JobView{
		ID: j.ID, Name: j.Name, Kind: j.Kind, CreatedBy: j.CreatedBy, Cadence: scheduler.Describe(j.Schedule, j.TZ), Schedule: j.Schedule,
		Prompt: j.Prompt, Enabled: j.State.Active(), LastStatus: j.LastStatus, LastError: j.LastError, LastStartedText: stamp(j.LastStarted, zone),
	}
	if j.State.Active() {
		v.NextDueText = stamp(j.NextDue, zone)
	}
	return v
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

func (p *pages) scheduledRoutes(r chi.Router) {
	r.Get("/scheduled", p.scheduledIndex)
	r.Post("/scheduled/{job_id}/toggle", p.scheduleToggle)
	r.Post("/scheduled/{job_id}/cancel", p.scheduleCancel)
	r.Post("/scheduled/{job_id}/schedule", p.scheduleEdit)
}

func (p *pages) scheduledData(r *http.Request) ScheduledData {
	ctx := detached(r)
	tz := config.From(ctx).TZName(p.deps.Env)
	zone, err := scheduler.Zone(tz)
	check(err)
	jobs, err := scheduler.ListJobs(ctx, p.deps.DB.Read, false)
	check(err)
	history := config.TuningWebRunHistory
	runs, err := scheduler.ListRuns(ctx, p.deps.DB.Read, 0, history)
	check(err)
	views := make([]JobView, len(jobs))
	for i, j := range jobs {
		views[i] = jobView(j, zone)
	}
	return ScheduledData{Jobs: views, Runs: runViews(runs, zone), TZ: tz}
}

func (p *pages) scheduledShow(w http.ResponseWriter, r *http.Request, status int, toast *tpl.Toast) {
	data := p.scheduledData(r)
	if web.IsPartial(r) {
		p.renderFragment(w, r, status, "_scheduled_list.gohtml", data, toast)
		return
	}
	p.render(w, r, status, "scheduled", "scheduled.gohtml", data, toast)
}

func (p *pages) loadJob(w http.ResponseWriter, r *http.Request, jobID int64) (*scheduler.Job, bool) {
	job, err := scheduler.GetJob(r.Context(), p.deps.DB.Read, jobID)
	check(err)
	if job == nil {
		p.fail(w, r, http.StatusNotFound, "unknown schedule")
		return nil, false
	}
	return job, true
}

func (p *pages) attempted(w http.ResponseWriter, r *http.Request, err error, success string) {
	var refused scheduler.Error
	if errors.As(err, &refused) {
		p.scheduledShow(w, r, http.StatusUnprocessableEntity, notice("error", "Not saved", err.Error()))
		return
	}
	check(err)
	if !web.IsPartial(r) {
		redirect(w, r, "/scheduled")
		return
	}
	p.scheduledShow(w, r, http.StatusOK, notice("success", success, ""))
}

func (p *pages) scheduledIndex(w http.ResponseWriter, r *http.Request) {
	p.scheduledShow(w, r, http.StatusOK, nil)
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
	enabled := !job.State.Active()
	_, err := scheduler.UpdateJob(detached(r), p.deps.DB.Write, id, p.now(), scheduler.JobUpdate{Enabled: &enabled, Actor: scheduler.Operator})
	p.attempted(w, r, err, "Schedule updated")
}

func (p *pages) scheduleEdit(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "job_id")
	if !ok {
		return
	}
	job, ok := p.loadJob(w, r, id)
	if !ok {
		return
	}
	schedule := strings.TrimSpace(web.FormString(web.FormValues(r), "schedule", ""))
	ctx := audit.With(detached(r), "operator", audit.UI)
	err := p.deps.DB.WithTx(ctx, func(tx *sql.Tx) error {
		if schedule == "" {
			return scheduler.Error("schedule is empty")
		}
		if def, known := scheduler.Declared(p.jobs, *job); known {
			if err := settings.From(ctx).Set(ctx, tx, def.ScheduleSetting, schedule); err != nil {
				return err
			}
		}
		_, err := scheduler.UpdateJob(ctx, tx, id, p.now(), scheduler.JobUpdate{Schedule: &schedule, Actor: scheduler.Operator})
		return err
	})
	var invalid settings.Invalid
	var pinned *settings.PinnedError
	if errors.As(err, &invalid) || errors.As(err, &pinned) {
		p.scheduledShow(w, r, http.StatusUnprocessableEntity, notice("error", "Not saved", err.Error()))
		return
	}
	p.attempted(w, r, err, "Schedule updated")
}

func (p *pages) scheduleCancel(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "job_id")
	if !ok {
		return
	}
	if _, ok := p.loadJob(w, r, id); !ok {
		return
	}
	p.attempted(w, r, scheduler.CancelJob(detached(r), p.deps.DB.Write, id), "Schedule deleted")
}

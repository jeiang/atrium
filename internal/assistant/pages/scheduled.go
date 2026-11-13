package pages

import (
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/jeiang/atrium/internal/application"
	asstapp "github.com/jeiang/atrium/internal/assistant/application"
	"github.com/jeiang/atrium/internal/web"
	"github.com/jeiang/atrium/internal/web/tpl"
)

type ScheduledData struct {
	Jobs []asstapp.ScheduleJob `json:"jobs"`
	Runs []asstapp.ScheduleRun `json:"runs"`
	TZ   string                `json:"tz"`
}

func (p *pages) scheduledRoutes(r chi.Router) {
	r.Get("/scheduled", p.scheduledIndex)
	r.Post("/scheduled/{job_id}/toggle", p.scheduleToggle)
	r.Post("/scheduled/{job_id}/cancel", p.scheduleCancel)
	r.Post("/scheduled/{job_id}/schedule", p.scheduleEdit)
}

func (p *pages) scheduledData(r *http.Request) ScheduledData {
	ctx := detached(r)
	jobs, err := p.assistant().ScheduleList(ctx, asstapp.ScheduleListInput{})
	check(err)
	runs, err := p.assistant().ScheduleHistory(ctx, asstapp.ScheduleHistoryInput{})
	check(err)
	return ScheduledData{Jobs: jobs.Data.Jobs, Runs: runs.Data, TZ: jobs.Data.TZ}
}

func (p *pages) scheduledShow(w http.ResponseWriter, r *http.Request, status int, toast *tpl.Toast) {
	data := p.scheduledData(r)
	if web.IsPartial(r) {
		p.renderFragment(w, r, status, "_scheduled_list.gohtml", data, toast)
		return
	}
	p.render(w, r, status, "scheduled", "scheduled.gohtml", data, toast)
}

func (p *pages) attempted(w http.ResponseWriter, r *http.Request, err error, success string) {
	var e *application.Error
	if errors.As(err, &e) {
		switch e.Code {
		case application.NotFound:
			p.fail(w, r, http.StatusNotFound, "unknown schedule")
			return
		case application.Invalid, application.Refused:
			p.scheduledShow(w, r, http.StatusUnprocessableEntity, notice("error", "Not saved", e.Message))
			return
		}
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
	_, err := p.assistant().ScheduleEnabled(detached(r), asstapp.ScheduleToggleInput{ID: id})
	p.attempted(w, r, err, "Schedule updated")
}

func (p *pages) scheduleEdit(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "job_id")
	if !ok {
		return
	}
	schedule := strings.TrimSpace(web.FormString(web.FormValues(r), "schedule", ""))
	_, err := p.assistant().ScheduleUpdate(detached(r), asstapp.ScheduleUpdateInput{ID: id, Schedule: &schedule})
	p.attempted(w, r, err, "Schedule updated")
}

func (p *pages) scheduleCancel(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "job_id")
	if !ok {
		return
	}
	_, err := p.assistant().ScheduleCancel(detached(r), asstapp.ScheduleCancelInput{ID: id})
	p.attempted(w, r, err, "Schedule deleted")
}

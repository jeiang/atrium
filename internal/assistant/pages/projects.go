package pages

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/assistant/files"
	"github.com/jeiang/atrium/internal/assistant/pricing"
	"github.com/jeiang/atrium/internal/assistant/projects"
	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/numx"
	"github.com/jeiang/atrium/internal/web"
)

type ProjectFormValues struct {
	Name         string  `json:"name"`
	Instructions string  `json:"instructions"`
	Effort       string  `json:"effort"`
	ContextLimit *int64  `json:"context_limit"`
	WikiHubID    *string `json:"wiki_hub_id"`
	Private      bool    `json:"private"`
}

type ProjectFormData struct {
	Project       *projects.Project `json:"project"`
	Values        ProjectFormValues `json:"values"`
	Efforts       []string          `json:"efforts"`
	ContextLimits []int64           `json:"context_limits"`
	Error         *string           `json:"error"`
}

type ProjectEditData struct {
	Form  ProjectFormData `json:"form"`
	Files []files.Row     `json:"files"`
}

func projectValues(found projects.Project) ProjectFormValues {
	limit := found.ContextLimit
	return ProjectFormValues{Name: found.Name, Instructions: found.Instructions, Effort: found.Effort, ContextLimit: &limit, WikiHubID: found.WikiHubID, Private: bool(found.Private)}
}

func submittedProject(form url.Values) projects.Fields {
	return projects.Fields{
		Name:         web.FormString(form, "name", ""),
		Instructions: web.FormString(form, "instructions", ""),
		Effort:       web.FormString(form, "effort", ""),
		ContextLimit: web.FormString(form, "context_limit", ""),
		WikiHubID:    web.FormString(form, "wiki_hub_id", ""),
		Private:      web.FormString(form, "private", "") == "1",
	}
}

func submittedValues(f projects.Fields, fallback ProjectFormValues) ProjectFormValues {
	hub := f.WikiHubID
	effort := fallback.Effort
	for _, choice := range pricing.Efforts {
		if f.Effort == choice {
			effort = f.Effort
			break
		}
	}
	limit := *fallback.ContextLimit
	if raw, ok := f.ContextLimit.(string); ok {
		if submitted, valid := numx.LaxInt64(raw); valid {
			for _, choice := range assistant.ContextLimits {
				if submitted == choice {
					limit = submitted
					break
				}
			}
		}
	}
	return ProjectFormValues{Name: f.Name, Instructions: f.Instructions, Effort: effort, ContextLimit: &limit, WikiHubID: &hub, Private: f.Private}
}

func editPath(id int64) string { return "/projects/" + strconv.FormatInt(id, 10) + "/edit" }

func (p *pages) projectRoutes(r chi.Router) {
	r.Get("/projects/new", p.projectNew)
	r.Post("/projects", p.projectCreate)
	r.Get("/projects/{project_id}/edit", p.projectEdit)
	r.Post("/projects/{project_id}/edit", p.projectUpdate)
	r.Post("/projects/{project_id}/archive", p.projectArchive)
}

func formData(project *projects.Project, values ProjectFormValues, problem *string) ProjectFormData {
	return ProjectFormData{Project: project, Values: values, Efforts: pricing.Efforts, ContextLimits: assistant.ContextLimits, Error: problem}
}

func (p *pages) defaultValues(r *http.Request) ProjectFormValues {
	chosen, err := threads.Defaults(r.Context(), p.deps.DB.Read)
	check(err)
	limit := chosen.ContextLimit
	return ProjectFormValues{Effort: chosen.Effort, ContextLimit: &limit}
}

func (p *pages) projectPage(w http.ResponseWriter, r *http.Request, status int, data ProjectFormData) {
	if data.Project == nil {
		p.render(w, r, status, "projects", "project_new.gohtml", data, nil)
		return
	}
	attached, err := files.OfProject(r.Context(), p.deps.DB.Read, data.Project.ID)
	check(err)
	if attached == nil {
		attached = []files.Row{}
	}
	p.render(w, r, status, "projects", "project_edit.gohtml", ProjectEditData{Form: data, Files: attached}, nil)
}

func (p *pages) projectNew(w http.ResponseWriter, r *http.Request) {
	p.projectPage(w, r, http.StatusOK, formData(nil, p.defaultValues(r), nil))
}

func (p *pages) projectEdit(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "project_id")
	if !ok {
		return
	}
	found, ok := p.loadProject(w, r, id)
	if !ok {
		return
	}
	p.projectPage(w, r, http.StatusOK, formData(found, projectValues(*found), nil))
}

func (p *pages) projectRefused(w http.ResponseWriter, r *http.Request, project *projects.Project, fields projects.Fields, err error) {
	fallback := p.defaultValues(r)
	if project != nil {
		fallback = projectValues(*project)
	}
	problem := err.Error()
	data := formData(project, submittedValues(fields, fallback), &problem)
	if web.IsPartial(r) {
		p.renderFragment(w, r, http.StatusUnprocessableEntity, "_project_form.gohtml", data, nil)
		return
	}
	p.projectPage(w, r, http.StatusUnprocessableEntity, data)
}

func (p *pages) projectCreate(w http.ResponseWriter, r *http.Request) {
	fields := submittedProject(web.FormValues(r))
	id, err := projects.Create(detached(r), p.deps.DB.Write, p.now(), fields)
	var refused projects.ProjectError
	if errors.As(err, &refused) {
		p.projectRefused(w, r, nil, fields, err)
		return
	}
	check(err)
	navigate(w, r, editPath(id))
}

func (p *pages) projectUpdate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "project_id")
	if !ok {
		return
	}
	fields := submittedProject(web.FormValues(r))
	found, ok := p.loadProject(w, r, id)
	if !ok {
		return
	}
	_, err := projects.Update(detached(r), p.deps.DB.Write, id, p.now(), fields)
	var refused projects.ProjectError
	if errors.As(err, &refused) {
		p.projectRefused(w, r, found, fields, err)
		return
	}
	check(err)
	navigate(w, r, editPath(id))
}

func (p *pages) projectArchive(w http.ResponseWriter, r *http.Request) {
	id, errs := pathInt(r, "project_id")
	flag := formInt(web.FormValues(r), "body", "archived", 1, &errs)
	if len(errs) > 0 {
		web.Unprocessable(w, errs...)
		return
	}
	found, err := projects.SetArchived(detached(r), p.deps.DB.Write, id, flag != 0)
	check(err)
	if !found {
		p.fail(w, r, http.StatusNotFound, "unknown project")
		return
	}
	to := editPath(id)
	if flag != 0 {
		to = "/chat"
	}
	navigate(w, r, to)
}

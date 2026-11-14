package pages

import (
	"net/http"
	"net/url"
	"slices"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/jeiang/atrium/internal/application"
	asstapp "github.com/jeiang/atrium/internal/assistant/application"
	"github.com/jeiang/atrium/internal/numx"
	"github.com/jeiang/atrium/internal/web"
)

type ProjectFormValues struct {
	Name         string `json:"name"`
	Instructions string `json:"instructions"`
	Effort       string `json:"effort"`
	ContextLimit *int64 `json:"context_limit"`
	Private      bool   `json:"private"`
}

type ProjectFormData struct {
	Project       *asstapp.Project  `json:"project"`
	Values        ProjectFormValues `json:"values"`
	Efforts       []string          `json:"efforts"`
	ContextLimits []int64           `json:"context_limits"`
	Error         *string           `json:"error"`
}

type ProjectEditData struct {
	Form  ProjectFormData       `json:"form"`
	Files []asstapp.ProjectFile `json:"files"`
}

func projectValues(found asstapp.Project) ProjectFormValues {
	limit := found.ContextLimit
	return ProjectFormValues{Name: found.Name, Instructions: found.Instructions, Effort: found.Effort, ContextLimit: &limit, Private: found.Private}
}

func submittedProject(form url.Values) asstapp.ProjectFields {
	return asstapp.ProjectFields{
		Name:         web.FormString(form, "name", ""),
		Instructions: web.FormString(form, "instructions", ""),
		Effort:       web.FormString(form, "effort", ""),
		ContextLimit: web.FormString(form, "context_limit", ""),
		Private:      web.FormString(form, "private", "") == "1",
	}
}

func submittedValues(cfg asstapp.ModelConfiguration, f asstapp.ProjectFields, fallback ProjectFormValues) ProjectFormValues {
	effort := fallback.Effort
	if slices.Contains(cfg.Efforts, f.Effort) {
		effort = f.Effort
	}
	limit := *fallback.ContextLimit
	if submitted, valid := numx.LaxInt64(f.ContextLimit); valid && slices.Contains(cfg.ContextLimits, submitted) {
		limit = submitted
	}
	return ProjectFormValues{Name: f.Name, Instructions: f.Instructions, Effort: effort, ContextLimit: &limit, Private: f.Private}
}

func editPath(id int64) string { return "/projects/" + strconv.FormatInt(id, 10) + "/edit" }

func (p *pages) projectRoutes(r chi.Router) {
	r.Get("/projects/new", p.projectNew)
	r.Post("/projects", p.projectCreate)
	r.Get("/projects/{project_id}/edit", p.projectEdit)
	r.Post("/projects/{project_id}/edit", p.projectUpdate)
	r.Post("/projects/{project_id}/archive", p.projectArchive)
}

func (p *pages) configuration(r *http.Request) asstapp.ModelConfiguration {
	read, err := p.assistant().Configuration(r.Context(), asstapp.ConfigurationInput{})
	check(err)
	return read.Data
}

func formData(cfg asstapp.ModelConfiguration, project *asstapp.Project, values ProjectFormValues, problem *string) ProjectFormData {
	return ProjectFormData{Project: project, Values: values, Efforts: cfg.Efforts, ContextLimits: cfg.ContextLimits, Error: problem}
}

func defaultValues(cfg asstapp.ModelConfiguration) ProjectFormValues {
	limit := cfg.Defaults.ContextLimit
	return ProjectFormValues{Effort: cfg.Defaults.Effort, ContextLimit: &limit}
}

func (p *pages) projectPage(w http.ResponseWriter, r *http.Request, status int, data ProjectFormData, attached []asstapp.ProjectFile) {
	if data.Project == nil {
		p.render(w, r, status, "projects", "project_new.gohtml", data, nil)
		return
	}
	p.render(w, r, status, "projects", "project_edit.gohtml", ProjectEditData{Form: data, Files: attached}, nil)
}

func (p *pages) projectNew(w http.ResponseWriter, r *http.Request) {
	cfg := p.configuration(r)
	p.projectPage(w, r, http.StatusOK, formData(cfg, nil, defaultValues(cfg), nil), nil)
}

func (p *pages) projectDetail(w http.ResponseWriter, r *http.Request) (asstapp.ProjectDetail, bool) {
	id, ok := pathID(w, r, "project_id")
	if !ok {
		return asstapp.ProjectDetail{}, false
	}
	read, err := p.assistant().ProjectDetail(r.Context(), asstapp.ProjectInput{ID: id})
	if err != nil {
		web.Fail(w, r, err, func(status int) { p.fail(w, r, status, application.Classify(err).Error()) })
		return asstapp.ProjectDetail{}, false
	}
	return read.Data, true
}

func (p *pages) projectEdit(w http.ResponseWriter, r *http.Request) {
	found, ok := p.projectDetail(w, r)
	if !ok {
		return
	}
	p.projectPage(w, r, http.StatusOK, formData(p.configuration(r), &found.Project, projectValues(found.Project), nil), found.Files)
}

func (p *pages) projectFailed(w http.ResponseWriter, r *http.Request, found *asstapp.ProjectDetail, fields asstapp.ProjectFields, err error) {
	web.Fail(w, r, err, func(status int) {
		problem := application.Classify(err).Error()
		if status != http.StatusUnprocessableEntity {
			p.fail(w, r, status, problem)
			return
		}
		cfg := p.configuration(r)
		fallback, project, attached := defaultValues(cfg), (*asstapp.Project)(nil), []asstapp.ProjectFile(nil)
		if found != nil {
			fallback, project, attached = projectValues(found.Project), &found.Project, found.Files
		}
		data := formData(cfg, project, submittedValues(cfg, fields, fallback), &problem)
		if web.IsPartial(r) {
			p.renderFragment(w, r, status, "_project_form.gohtml", data, nil)
			return
		}
		p.projectPage(w, r, status, data, attached)
	})
}

func (p *pages) projectCreate(w http.ResponseWriter, r *http.Request) {
	fields := submittedProject(web.FormValues(r))
	changed, err := p.assistant().ProjectCreate(detached(r), fields)
	if err != nil {
		p.projectFailed(w, r, nil, fields, err)
		return
	}
	navigate(w, r, editPath(changed.Data.ID))
}

func (p *pages) projectUpdate(w http.ResponseWriter, r *http.Request) {
	found, ok := p.projectDetail(w, r)
	if !ok {
		return
	}
	fields := submittedProject(web.FormValues(r))
	if _, err := p.assistant().ProjectEdit(detached(r), asstapp.ProjectEditInput{ID: found.Project.ID, ProjectFields: fields}); err != nil {
		p.projectFailed(w, r, &found, fields, err)
		return
	}
	navigate(w, r, editPath(found.Project.ID))
}

func (p *pages) projectArchive(w http.ResponseWriter, r *http.Request) {
	id, errs := pathInt(r, "project_id")
	flag := formInt(web.FormValues(r), "body", "archived", 1, &errs)
	if len(errs) > 0 {
		web.Unprocessable(w, errs...)
		return
	}
	if _, err := p.assistant().ProjectArchive(detached(r), asstapp.ProjectArchiveInput{ID: id, Archived: flag != 0}); err != nil {
		web.Fail(w, r, err, func(status int) { p.fail(w, r, status, application.Classify(err).Error()) })
		return
	}
	to := editPath(id)
	if flag != 0 {
		to = "/chat"
	}
	navigate(w, r, to)
}

package pages

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/jeiang/atrium/internal/assistant/files"
	"github.com/jeiang/atrium/internal/assistant/projects"
	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/web"
)

const projectThreadLimit = 100

type ProjectsData struct {
	Projects []projects.Project `json:"projects"`
	View     string             `json:"view"`
}

type ProjectData struct {
	Project *projects.Project `json:"project"`
	Files   []files.Row       `json:"files"`
	Threads []threads.Listed  `json:"threads"`
	View    string            `json:"view"`
}

type ProjectFormValues struct {
	Name         string  `json:"name"`
	Instructions string  `json:"instructions"`
	Effort       string  `json:"effort"`
	ContextLimit *int64  `json:"context_limit"`
	WikiHubID    *string `json:"wiki_hub_id"`
}

type ProjectFormData struct {
	Project       *projects.Project `json:"project"`
	Values        ProjectFormValues `json:"values"`
	Efforts       []string          `json:"efforts"`
	ContextLimits []int64           `json:"context_limits"`
	Error         *string           `json:"error"`
}

func projectValues(found projects.Project) ProjectFormValues {
	limit := found.ContextLimit
	return ProjectFormValues{Name: found.Name, Instructions: found.Instructions, Effort: found.Effort, ContextLimit: &limit, WikiHubID: found.WikiHubID}
}

func submittedProject(form url.Values) projects.Fields {
	return projects.Fields{
		Name:         web.FormString(form, "name", ""),
		Instructions: web.FormString(form, "instructions", ""),
		Effort:       web.FormString(form, "effort", ""),
		ContextLimit: web.FormString(form, "context_limit", ""),
		WikiHubID:    web.FormString(form, "wiki_hub_id", ""),
	}
}

func submittedValues(f projects.Fields, fallback ProjectFormValues) ProjectFormValues {
	hub := f.WikiHubID
	effort := fallback.Effort
	for _, choice := range threads.Efforts {
		if f.Effort == choice {
			effort = f.Effort
			break
		}
	}
	limit := *fallback.ContextLimit
	if submitted, ok := py.LaxInt64(f.ContextLimit); ok {
		for _, choice := range threads.ContextLimits {
			if submitted == choice {
				limit = submitted
				break
			}
		}
	}
	return ProjectFormValues{Name: f.Name, Instructions: f.Instructions, Effort: effort, ContextLimit: &limit, WikiHubID: &hub}
}

func validView(view string) bool { return view == "" || view == archived }

func (p *pages) projectRoutes(r chi.Router) {
	r.Get("/projects", p.projectsIndex)
	r.Get("/projects/new", p.projectNewForm)
	r.Post("/projects", p.projectCreate)
	r.Get("/projects/{project_id}", p.projectShow)
	r.Get("/projects/{project_id}/edit", p.projectEditForm)
	r.Post("/projects/{project_id}", p.projectUpdate)
	r.Post("/projects/{project_id}/archive", p.projectArchive)
}

func (p *pages) projectsIndex(w http.ResponseWriter, r *http.Request) {
	view := web.QueryValue(r.URL.Query(), "view")
	if !validView(view) {
		p.fail(w, r, http.StatusUnprocessableEntity, "unknown view")
		return
	}
	found, err := projects.List(r.Context(), p.deps.DB.Read, view == archived)
	check(err)
	p.render(w, r, http.StatusOK, "projects", "projects.gohtml", ProjectsData{Projects: found, View: view}, nil)
}

func (p *pages) projectForm(w http.ResponseWriter, r *http.Request, status int, project *projects.Project, values ProjectFormValues, problem *string) {
	data := ProjectFormData{Project: project, Values: values, Efforts: threads.Efforts[:], ContextLimits: threads.ContextLimits[:], Error: problem}
	p.renderFragment(w, r, status, "_project_form.gohtml", data, nil)
}

func (p *pages) projectNewForm(w http.ResponseWriter, r *http.Request) {
	chosen, err := threads.Defaults(r.Context(), p.deps.DB.Read)
	check(err)
	limit := chosen.ContextLimit
	p.projectForm(w, r, http.StatusOK, nil, ProjectFormValues{Effort: chosen.Effort, ContextLimit: &limit}, nil)
}

func (p *pages) projectRefused(w http.ResponseWriter, r *http.Request, project *projects.Project, fields projects.Fields, err error) {
	if !web.IsPartial(r) {
		p.fail(w, r, http.StatusUnprocessableEntity, err.Error())
		return
	}
	fallback := ProjectFormValues{}
	if project != nil {
		fallback = projectValues(*project)
	} else {
		chosen, loadErr := threads.Defaults(r.Context(), p.deps.DB.Read)
		check(loadErr)
		limit := chosen.ContextLimit
		fallback = ProjectFormValues{Effort: chosen.Effort, ContextLimit: &limit}
	}
	problem := err.Error()
	p.projectForm(w, r, http.StatusUnprocessableEntity, project, submittedValues(fields, fallback), &problem)
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
	navigate(w, r, "/projects/"+strconv.FormatInt(id, 10))
}

func (p *pages) projectShow(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "project_id")
	if !ok {
		return
	}
	view := web.QueryValue(r.URL.Query(), "view")
	if !validView(view) {
		p.fail(w, r, http.StatusUnprocessableEntity, "unknown view")
		return
	}
	found, ok := p.loadProject(w, r, id)
	if !ok {
		return
	}
	listed, err := threads.ListThreads(r.Context(), p.deps.DB.Read, threads.ListOpts{Limit: projectThreadLimit, Archived: view == archived, ProjectID: id})
	check(err)
	attached, err := files.OfProject(r.Context(), p.deps.DB.Read, id)
	check(err)
	if attached == nil {
		attached = []files.Row{}
	}
	p.render(w, r, http.StatusOK, "projects", "project.gohtml", ProjectData{Project: found, Files: attached, Threads: listed, View: view}, nil)
}

func (p *pages) projectEditForm(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "project_id")
	if !ok {
		return
	}
	found, ok := p.loadProject(w, r, id)
	if !ok {
		return
	}
	p.projectForm(w, r, http.StatusOK, found, projectValues(*found), nil)
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
	navigate(w, r, "/projects/"+strconv.FormatInt(id, 10))
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
	to := "/projects/" + strconv.FormatInt(id, 10)
	if flag != 0 {
		to = "/projects"
	}
	navigate(w, r, to)
}

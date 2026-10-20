package pages

import (
	"database/sql"
	"errors"
	"net/http"
	"net/url"

	"github.com/jeiang/atrium/internal/assistant/subagents"
	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/web"
	"github.com/jeiang/atrium/internal/web/tpl"
)

type AgentView struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Prompt      string   `json:"prompt"`
	Effort      string   `json:"effort"`
	Enabled     bool     `json:"enabled"`
	Builtin     bool     `json:"builtin"`
	Tools       []string `json:"tools"`
}

type SubagentsData struct {
	Agents      []AgentView          `json:"agents"`
	Pool        []subagents.PoolTool `json:"pool"`
	Efforts     []string             `json:"efforts"`
	Draft       AgentView            `json:"draft"`
	Editing     string               `json:"editing"`
	Error       string               `json:"error"`
	Focus       string               `json:"focus"`
	StripParams bool                 `json:"strip_params"`
}

func agentView(a subagents.Agent) AgentView {
	return AgentView{Name: a.Name, Description: a.Description, Prompt: a.Prompt, Effort: a.Effort, Enabled: a.Enabled, Builtin: a.Builtin, Tools: a.Tools}
}

func (p *pages) subagentsPage(w http.ResponseWriter, r *http.Request, status int, toast *tpl.Toast, draft AgentView, editing, formError, focus string, stripParams bool) {
	found, err := subagents.List(r.Context(), p.deps.DB.Read)
	check(err)
	pool, err := subagents.Pool(p.reg)
	check(err)
	views := make([]AgentView, len(found))
	for i, a := range found {
		views[i] = agentView(a)
	}
	p.settings(w, r, status, "subagents.gohtml", SubagentsData{
		Agents: views, Pool: pool, Efforts: threads.Efforts[:], Draft: draft, Editing: editing, Error: formError, Focus: focus, StripParams: stripParams,
	}, toast)
}

func (p *pages) subagentsIndex(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var toast *tpl.Toast
	var focus string
	saved := web.QueryValue(r.URL.Query(), "saved")
	if saved != "" {
		agent, err := subagents.Get(ctx, p.deps.DB.Read, saved)
		check(err)
		if agent != nil {
			focus = saved
			toast = notice("success", "Subagent saved", saved)
		}
	}
	toggled := web.QueryValue(r.URL.Query(), "toggled")
	if toast == nil && toggled != "" {
		agent, err := subagents.Get(ctx, p.deps.DB.Read, toggled)
		check(err)
		if agent != nil {
			state := "disabled"
			if agent.Enabled {
				state = "enabled"
			}
			toast = notice("success", "Subagent updated", agent.Name+" is now "+state)
		}
	}
	deleted := web.QueryValue(r.URL.Query(), "deleted")
	if toast == nil && deleted != "" {
		agent, err := subagents.Get(ctx, p.deps.DB.Read, deleted)
		check(err)
		if agent == nil {
			toast = notice("success", "Subagent deleted", deleted)
		}
	}
	p.subagentsPage(w, r, http.StatusOK, toast, AgentView{}, "", "", focus, toast != nil)
}

func (p *pages) subagentSave(w http.ResponseWriter, r *http.Request) {
	form := web.FormValues(r)
	draft := AgentView{
		Name: web.FormString(form, "name", ""), Description: web.FormString(form, "description", ""), Prompt: web.FormString(form, "prompt", ""),
		Effort: web.FormString(form, "effort", threads.Medium), Tools: form["tools"],
	}
	ctx := detached(r)
	err := p.deps.DB.WithTx(ctx, func(tx *sql.Tx) error {
		existing, err := subagents.Get(ctx, tx, py.Strip(draft.Name))
		if err != nil {
			return err
		}
		_, err = subagents.Save(ctx, tx, p.reg, subagents.Definition{
			Name: draft.Name, Description: draft.Description, Prompt: draft.Prompt, Effort: draft.Effort, Tools: draft.Tools,
			Enabled: existing == nil || existing.Enabled,
		}, p.now())
		return err
	})
	var refused subagents.Error
	if errors.As(err, &refused) {
		editing := ""
		if web.FormString(form, "from", "") == "edit" {
			existing, getErr := subagents.Get(ctx, p.deps.DB.Read, py.Strip(draft.Name))
			check(getErr)
			if existing != nil {
				editing = existing.Name
			}
		}
		p.subagentsPage(w, r, http.StatusUnprocessableEntity, notice("error", "Not saved", err.Error()), draft, editing, err.Error(), "", false)
		return
	}
	check(err)
	settingsRedirect(w, "/settings/subagents?saved="+url.QueryEscape(py.Strip(draft.Name)))
}

func (p *pages) subagentToggle(w http.ResponseWriter, r *http.Request) {
	name := web.PathParam(r, "name")
	ctx := detached(r)
	known := false
	err := p.deps.DB.WithTx(ctx, func(tx *sql.Tx) error {
		found, err := subagents.Get(ctx, tx, name)
		if err != nil || found == nil {
			return err
		}
		known = true
		_, err = subagents.SetEnabled(ctx, tx, name, !found.Enabled, p.now())
		return err
	})
	check(err)
	if !known {
		p.fail(w, r, http.StatusNotFound, "unknown subagent")
		return
	}
	settingsRedirect(w, "/settings/subagents?toggled="+url.QueryEscape(name))
}

func (p *pages) subagentDelete(w http.ResponseWriter, r *http.Request) {
	name := web.PathParam(r, "name")
	removed, err := subagents.Delete(detached(r), p.deps.DB.Write, name)
	var refused subagents.Error
	if errors.As(err, &refused) {
		p.subagentsPage(w, r, http.StatusUnprocessableEntity, notice("error", "Not deleted", err.Error()), AgentView{}, "", "", "", false)
		return
	}
	check(err)
	if !removed {
		p.fail(w, r, http.StatusNotFound, "unknown subagent")
		return
	}
	settingsRedirect(w, "/settings/subagents?deleted="+url.QueryEscape(name))
}

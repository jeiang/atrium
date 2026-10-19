package pages

import (
	"database/sql"
	"errors"
	"net/http"

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
	Agents  []AgentView          `json:"agents"`
	Pool    []subagents.PoolTool `json:"pool"`
	Efforts []string             `json:"efforts"`
	Draft   AgentView            `json:"draft"`
}

func agentView(a subagents.Agent) AgentView {
	return AgentView{Name: a.Name, Description: a.Description, Prompt: a.Prompt, Effort: a.Effort, Enabled: a.Enabled, Builtin: a.Builtin, Tools: a.Tools}
}

func (p *pages) subagentsPage(w http.ResponseWriter, r *http.Request, status int, toast *tpl.Toast, draft AgentView) {
	found, err := subagents.List(r.Context(), p.deps.DB.Read)
	check(err)
	pool, err := subagents.Pool(p.reg)
	check(err)
	views := make([]AgentView, len(found))
	for i, a := range found {
		views[i] = agentView(a)
	}
	p.settings(w, r, status, "subagents.gohtml", SubagentsData{Agents: views, Pool: pool, Efforts: threads.Efforts[:], Draft: draft}, toast)
}

func (p *pages) subagentsIndex(w http.ResponseWriter, r *http.Request) {
	p.subagentsPage(w, r, http.StatusOK, nil, AgentView{})
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
		p.subagentsPage(w, r, http.StatusUnprocessableEntity, notice("error", "Not saved", err.Error()), draft)
		return
	}
	check(err)
	p.subagentsPage(w, r, http.StatusOK, notice("success", "Subagent saved", py.Strip(draft.Name)), AgentView{})
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
	p.subagentsPage(w, r, http.StatusOK, notice("success", "Subagent updated", name), AgentView{})
}

func (p *pages) subagentDelete(w http.ResponseWriter, r *http.Request) {
	name := web.PathParam(r, "name")
	removed, err := subagents.Delete(detached(r), p.deps.DB.Write, name)
	var refused subagents.Error
	if errors.As(err, &refused) {
		p.subagentsPage(w, r, http.StatusUnprocessableEntity, notice("error", "Not deleted", err.Error()), AgentView{})
		return
	}
	check(err)
	if !removed {
		p.fail(w, r, http.StatusNotFound, "unknown subagent")
		return
	}
	p.subagentsPage(w, r, http.StatusOK, notice("success", "Subagent deleted", name), AgentView{})
}
